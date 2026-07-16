// QUIC Initial — извлечение SNI из первого пакета QUIC-соединения (HTTP/3).
//
// QUIC Initial-пакеты «зашифрованы», но ключ выводится из Destination Connection
// ID по фиксированной соли (RFC 9001 §5.2) — то есть расшифровать Initial и достать
// TLS ClientHello (а из него SNI) может кто угодно, кто видит пакет. Мы это делаем
// пассивно на WAN, чтобы ловить блокировки HTTP/3, которые TCP-детектор не видит.
package watcher

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

// соль для QUIC v1 initial secrets (RFC 9001 §5.2).
var quicInitialSalt = []byte{
	0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17,
	0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a,
}

// hkdfExtract — HKDF-Extract на HMAC-SHA256 (RFC 5869).
func hkdfExtract(salt, ikm []byte) []byte {
	h := hmac.New(sha256.New, salt)
	h.Write(ikm)
	return h.Sum(nil)
}

// hkdfExpandLabel — HKDF-Expand-Label в стиле TLS 1.3 / QUIC (RFC 8446 §7.1).
func hkdfExpandLabel(secret []byte, label string, length int) []byte {
	full := "tls13 " + label
	var info []byte
	info = append(info, byte(length>>8), byte(length))
	info = append(info, byte(len(full)))
	info = append(info, full...)
	info = append(info, 0x00) // пустой context
	// HKDF-Expand (одна итерация достаточна: length <= 32)
	h := hmac.New(sha256.New, secret)
	h.Write(info)
	h.Write([]byte{0x01})
	out := h.Sum(nil)
	if length > len(out) {
		length = len(out)
	}
	return out[:length]
}

// readVarint — QUIC variable-length integer (RFC 9000 §16). Возвращает значение и
// число прочитанных байт.
func readVarint(b []byte) (uint64, int) {
	if len(b) == 0 {
		return 0, 0
	}
	prefix := b[0] >> 6
	l := 1 << prefix
	if len(b) < l {
		return 0, 0
	}
	v := uint64(b[0] & 0x3f)
	for i := 1; i < l; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v, l
}

// isQUICClientInitial — грубая проверка: long header, тип Initial, версия QUIC v1.
func isQUICClientInitial(p []byte) bool {
	if len(p) < 5 {
		return false
	}
	if p[0]&0x80 == 0 || p[0]&0x40 == 0 { // long header + fixed bit
		return false
	}
	if (p[0]&0x30)>>4 != 0 { // тип пакета Initial == 0
		return false
	}
	return p[1] == 0x00 && p[2] == 0x00 && p[3] == 0x00 && p[4] == 0x01 // версия 1
}

// parseQUICInitialSNI — расшифровать QUIC Initial и вернуть SNI из ClientHello.
// Возвращает "" если это не клиентский Initial / не удалось разобрать.
func parseQUICInitialSNI(p []byte) string {
	if !isQUICClientInitial(p) {
		return ""
	}
	off := 5
	if off >= len(p) {
		return ""
	}
	dcidLen := int(p[off])
	off++
	if off+dcidLen > len(p) {
		return ""
	}
	dcid := p[off : off+dcidLen]
	off += dcidLen
	if off >= len(p) {
		return ""
	}
	scidLen := int(p[off])
	off++
	off += scidLen
	if off >= len(p) {
		return ""
	}
	tokLen, n := readVarint(p[off:])
	if n == 0 {
		return ""
	}
	off += n + int(tokLen)
	if off >= len(p) {
		return ""
	}
	length, n := readVarint(p[off:])
	if n == 0 {
		return ""
	}
	off += n
	pnOffset := off
	if pnOffset+4+16 > len(p) || pnOffset+int(length) > len(p) {
		return ""
	}

	// вывод ключей из DCID
	initialSecret := hkdfExtract(quicInitialSalt, dcid)
	clientSecret := hkdfExpandLabel(initialSecret, "client in", 32)
	key := hkdfExpandLabel(clientSecret, "quic key", 16)
	iv := hkdfExpandLabel(clientSecret, "quic iv", 12)
	hp := hkdfExpandLabel(clientSecret, "quic hp", 16)

	// снятие header protection: маска = AES-ECB(hp, sample)
	blk, err := aes.NewCipher(hp)
	if err != nil {
		return ""
	}
	sample := p[pnOffset+4 : pnOffset+4+16]
	mask := make([]byte, 16)
	blk.Encrypt(mask, sample)

	hdr := make([]byte, pnOffset+4)
	copy(hdr, p[:pnOffset+4])
	hdr[0] ^= mask[0] & 0x0f
	pnLen := int(hdr[0]&0x03) + 1
	hdr = hdr[:pnOffset+pnLen]
	var pn uint64
	for i := 0; i < pnLen; i++ {
		hdr[pnOffset+i] = p[pnOffset+i] ^ mask[1+i]
		pn = pn<<8 | uint64(hdr[pnOffset+i])
	}

	// nonce = iv XOR packet number (right-aligned)
	nonce := make([]byte, 12)
	copy(nonce, iv)
	var pnb [8]byte
	binary.BigEndian.PutUint64(pnb[:], pn)
	for i := 0; i < 8; i++ {
		nonce[11-i] ^= pnb[7-i]
	}

	aead, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}
	gcm, err := cipher.NewGCM(aead)
	if err != nil {
		return ""
	}
	ciphertext := p[pnOffset+pnLen : pnOffset+int(length)]
	plain, err := gcm.Open(nil, nonce, ciphertext, hdr)
	if err != nil {
		return ""
	}
	return sniFromCryptoFrames(plain)
}

// sniFromCryptoFrames — собрать CRYPTO-фреймы (по offset) и вытащить SNI из
// TLS ClientHello. ClientHello обычно целиком в первом Initial с offset 0.
func sniFromCryptoFrames(frames []byte) string {
	var buf []byte // реассемблированный handshake (по offset)
	i := 0
	for i < len(frames) {
		ft, n := readVarint(frames[i:])
		if n == 0 {
			break
		}
		i += n
		switch ft {
		case 0x00, 0x01: // PADDING, PING — один байт
			continue
		case 0x02, 0x03: // ACK — пропускаем корректно нельзя без разбора, выходим
			return parseCH(buf)
		case 0x06: // CRYPTO: offset varint, length varint, data
			offVal, n1 := readVarint(frames[i:])
			i += n1
			ln, n2 := readVarint(frames[i:])
			i += n2
			if n1 == 0 || n2 == 0 || i+int(ln) > len(frames) {
				return parseCH(buf)
			}
			data := frames[i : i+int(ln)]
			i += int(ln)
			end := int(offVal) + len(data)
			if end > len(buf) {
				nb := make([]byte, end)
				copy(nb, buf)
				buf = nb
			}
			copy(buf[offVal:], data)
		default:
			return parseCH(buf) // неизвестный фрейм — что собрали, то и парсим
		}
	}
	return parseCH(buf)
}

// parseCH — SNI из TLS ClientHello (сырой handshake без record-заголовка).
// parseSNI ожидает 5-байтный record-префикс — добавляем фиктивный.
func parseCH(hs []byte) string {
	if len(hs) < 4 || hs[0] != 0x01 {
		return ""
	}
	sni := parseSNI(append([]byte{0x16, 0x03, 0x01, 0x00, 0x00}, hs...))
	if !isHostname(sni) {
		return "" // защита от мусора при рассинхроне парсинга
	}
	return sni
}

// isHostname — SNI должен выглядеть как доменное имя: [a-z0-9.-], точка, без мусора.
func isHostname(s string) bool {
	if len(s) < 3 || len(s) > 253 {
		return false
	}
	dot := false
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		case c == '.':
			dot = true
		default:
			return false
		}
	}
	return dot
}
