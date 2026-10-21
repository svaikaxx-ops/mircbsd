package main

// Ekrano pločio matavimas.
//
// Terminalo ląstelė (ekrano stulpelis) nėra lygi vienam Unicode simboliui:
//   - CJK ideogramai (kinų, japonų, korėjiečių) užima 2 ląsteles;
//   - diakritiniai ženklai (ą, č, ų, ū) ir derinamieji simboliai (emoji
//     odų tonai, ZWJ, variantų selektoriai) — 0 ląstelių;
//   - paprastos raidės ir skaičiai — 1 ląstelė.
//
// Matavimas rune'ais (utf8.RuneCountInString) todėl gali skirtis nuo tikrojo
// pločio: 40 CJK ideogramų = 80 ląstelių, bet tik 40 rune'ų. Eilutė perlipa
// terminalo plotį ir „baga“ šoninę juostą.
//
// Šis modulis nėra priklausomybė — pločio intervalai įrašyti rankiniu būdu
// (Unicode East Asian Width + emoji), nes projektas laikosi minimalių
// priklausomybių (tik golang.org/x/term).

import (
	"strings"
	"unicode"
)

// ---- Platūs (2 ląstelės) intervalai ----

// wideRanges — intervalai, kuriuose simboliai užima 2 terminalo ląsteles
// (East Asian Wide + Fullwidth + emoji).
var wideRanges = [][2]rune{
	{0x1100, 0x115F},   // Hangul Jamo pradinės raidės
	{0x231A, 0x231B},   // ⌚⌛
	{0x2329, 0x232A},   // ⟨⟩
	{0x23E9, 0x23EC},   // ⏩⏪⏫⏬
	{0x23F0, 0x23F0},   // ⏰
	{0x23F3, 0x23F3},   // ⏳
	{0x25FD, 0x25FE},   // ◽◾
	{0x2614, 0x2615},   // ☔☕
	{0x2648, 0x2653},   // ♈♉♊♋♌
	{0x267F, 0x267F},   // ♿
	{0x2693, 0x2693},   // ⚓
	{0x26A1, 0x26A1},   // ⚡
	{0x26AA, 0x26AB},   // ⚪⚫
	{0x26BD, 0x26BE},   // ⚽⚾
	{0x26C4, 0x26C5},   // ⛄⛅
	{0x26CE, 0x26CE},   // ⛎
	{0x26D4, 0x26D4},   // ⛔
	{0x26EA, 0x26EA},   // ⛺
	{0x26F2, 0x26F3},   // ⛲⛳
	{0x26F5, 0x26F5},   // ⛵
	{0x26FA, 0x26FA},   // ⛺
	{0x26FD, 0x26FD},   // ⛽
	{0x2705, 0x2705},   // ✅
	{0x270A, 0x270B},   // ✊✋
	{0x2728, 0x2728},   // ✨
	{0x274C, 0x274C},   // ❌
	{0x274E, 0x274E},   // ❎
	{0x2753, 0x2755},   // ❓❔❕
	{0x2757, 0x2757},   // ❗
	{0x2795, 0x2797},   // ➕➖➗
	{0x27B0, 0x27B0},   // ➰
	{0x27BF, 0x27BF},   // ➿
	{0x2B1B, 0x2B1C},   // ⬛⬜
	{0x2B50, 0x2B50},   // ⭐
	{0x2B55, 0x2B55},   // ⭕
	{0x2E80, 0x2E99},   // CJK radikaliai, Kangxi
	{0x2E9B, 0x2EF3},   // Kangxi radikaliai
	{0x2F00, 0x2FD5},   // Kangxi ideogramai
	{0x2FF0, 0x2FFB},   // ideografiniai aprašai
	{0x3000, 0x303E},   // CJK simboliai, Hiragana, Katakana
	{0x3041, 0x3096},   // Hiragana
	{0x309A, 0x30FF},   // Katakana
	{0x3105, 0x312F},   // Bopomofo
	{0x3131, 0x318E},   // Hangul sujungimo
	{0x3190, 0x31E3},   // Bopomofo plėtiniai, Kana
	{0x31F0, 0x321E},   // Kana, Katakana
	{0x3220, 0x3247},   // Enclosed CJK
	{0x3250, 0x4DBF},   // Enclosed CJK, CJK plėtinys A
	{0x4E00, 0xA4CF},   // CJK suvienyti ideogramai, Yi
	{0xA960, 0xA97C},   // Hangul Jamo plėtinys A
	{0xAC00, 0xD7A3},   // Hangul skiemenys
	{0xF900, 0xFAFF},   // CJK suderinamumo ideogramai
	{0xFE10, 0xFE19},   // Vertikalios formos
	{0xFE30, 0xFE52},   // CJK suderinamumo formos
	{0xFE54, 0xFE66},   // Mažos formos
	{0xFE68, 0xFE6B},   // Mažos formos
	{0xFF01, 0xFF60},   // Pliūs formos (！～)
	{0xFFE0, 0xFFE6},   // Pliūs ženklai
	{0x16FE0, 0x16FE4}, // Tangut
	{0x17000, 0x18AFF}, // Tangut plėtinys
	{0x1B000, 0x1B2FF}, // Kana plėtinys
	{0x1F004, 0x1F004}, // 🀄
	{0x1F0CF, 0x1F0CF}, // 🃏
	{0x1F18E, 0x1F18E}, // 🆎
	{0x1F191, 0x1F19A}, // 🆑-🆚
	{0x1F1E6, 0x1F1FF}, // regioniniai rodikliai (vėliavos)
	{0x1F200, 0x1F320}, // 🈀-🌠
	{0x1F32D, 0x1F335}, // 🌭-🌵
	{0x1F337, 0x1F37C}, // 🌷-🎌
	{0x1F37E, 0x1F393}, // 🎎-🎓
	{0x1F3A0, 0x1F3CA}, // 🎠-🎊
	{0x1F3CF, 0x1F3D3}, // 🎏-🎓
	{0x1F3E0, 0x1F3F0}, // 🎠-🏰
	{0x1F3F4, 0x1F3F4}, // 🏴
	{0x1F3F8, 0x1F43E}, // 🏸-🐾
	{0x1F440, 0x1F440}, // 🐀
	{0x1F442, 0x1F4FC}, // 🐂-🗼
	{0x1F4FF, 0x1F53D}, // 🗿-🔽
	{0x1F54B, 0x1F54E}, // 🕋-🕎
	{0x1F550, 0x1F567}, // 🕐-🕧
	{0x1F57A, 0x1F57A}, // 🕺
	{0x1F595, 0x1F596}, // 🖕-🖖
	{0x1F5A4, 0x1F5A4}, // 🖤
	{0x1F5FB, 0x1F64F}, // 🖻-🙏
	{0x1F680, 0x1F6C5}, // 🚀-🚅
	{0x1F6CC, 0x1F6CC}, // 🚌
	{0x1F6D0, 0x1F6D2}, // 🚐-🚒
	{0x1F6D5, 0x1F6D7}, // 🚕-🚗
	{0x1F6DC, 0x1F6DF}, // 🚘-🚟
	{0x1F6EB, 0x1F6EC}, // 🚫-🚬
	{0x1F6F4, 0x1F6FC}, // 🚴-🚼
	{0x1F7E0, 0x1F7EB}, // 🟠-🟫
	{0x1F7F0, 0x1F7F0}, // 🟰
	{0x1F90C, 0x1F93A}, // 🤌-🤺
	{0x1F93C, 0x1F945}, // 🤼-🥅
	{0x1F947, 0x1F9FF}, // 🥇-🧿
	{0x1FA70, 0x1FAFF}, // 🩰-🫿
	{0x20000, 0x2FFFD}, // CJK plėtinys B ir daugiau
	{0x30000, 0x3FFFD}, // CJK plėtinys G+
}

// zeroWidthRanges — intervalai, kurie nematomi (derinamieji ženklai).
var zeroWidthRanges = [][2]rune{
	{0x0300, 0x036F},   // derinamieji diakritiniai ženklai
	{0x0483, 0x0489},   // Kirilicos derinamieji
	{0x0591, 0x05BD},   // hebrajiečių
	{0x0610, 0x061A},   // arabų
	{0x064B, 0x065F},   // arabų
	{0x0670, 0x0670},   // arabų
	{0x06D6, 0x06DC},   // arabų
	{0x0730, 0x074A},   // kurdų
	{0x07A6, 0x07B0},   // arabų
	{0x0900, 0x0903},   // devanagari
	{0x093A, 0x093C},   // devanagari
	{0x0941, 0x0948},   // devanagari
	{0x094D, 0x094D},   // devanagari
	{0x0951, 0x0957},   // devanagari
	{0x0962, 0x0963},   // devanagari
	{0x0E31, 0x0E31},   // tailandai
	{0x0E34, 0x0E3A},   // tailandai
	{0x0E47, 0x0E4E},   // tailandai
	{0x1AB0, 0x1AFF},   // derinamieji
	{0x1DC0, 0x1DFF},   // derinamieji
	{0x200B, 0x200F},   // zero-width space..RLM (incl. ZWJ 0x200D)
	{0x2028, 0x202E},   // eilutės/teksto nukreipimai
	{0x2060, 0x2064},   // neapmatomi formatavimo simboliai
	{0x20D0, 0x20F0},   // derinamieji simboliai
	{0x3099, 0x3099},   // Katakana-Hiragana balsingo ženklas
	{0xFE00, 0xFE0F},   // variantų selektoriai (emoji)
	{0xFE20, 0xFE2F},   // pusiau pliūs formos
	{0xFEFF, 0xFEFF},   // BOM
	{0x1F3FB, 0x1F3FF}, // emoji odų tonai (derinamieji)
	{0xE0100, 0xE01EF}, // variantų selektoriai, papildomi
}

// inRanges patikrina, ar r falls į kurį nors intervalą.
func inRanges(r rune, ranges [][2]rune) bool {
	lo, hi := 0, len(ranges)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case r < ranges[mid][0]:
			hi = mid - 1
		case r > ranges[mid][1]:
			lo = mid + 1
		default:
			return true
		}
	}
	return false
}

// runeWidth grąžina simbolio plotį terminalo ląstelėmis.
func runeWidth(r rune) int {
	if r == 0 {
		return 0
	}
	if r < 32 || (r >= 0x7F && r < 0xA0) {
		// Valdymo simboliai — 0 plotis (jie vis tiek pašalinami)
		return 0
	}
	if r < 0x0300 {
		// Greitoji peržiūra: ASCII, Graicinės ir Lotynų plėtinys A —
		// visi 1 ląstelės. Derinamieji prasideda nuo 0x0300.
		return 1
	}
	if inRanges(r, zeroWidthRanges) {
		return 0
	}
	if unicode.In(r, unicode.Mn, unicode.Me) {
		return 0
	}
	if inRanges(r, wideRanges) {
		return 2
	}
	return 1
}

// stringWidth grąžina teksto plotį terminalo ląstelėmis.
//
// Sąmoninga: plotis skaičiuojamas GRAFINIAIS KLASTERIAIS (viena emoji seka —
// 2 ląstelės, ne 6), todėl atitinka clusterWidth.
func stringWidth(s string) int {
	return clustersWidth(runeClusters(s))
}

// runeClusters suskaiddo tekstą į grafinius klasterius (grapheme clusters).
// Klasteris — matomas simbolis: raidė + jos diakritiniai ženklai, arba emoji
// (su odų tonu / ZWJ jungtimi). Trūkinant klasterį per vidurį, ekrane
// atsiranda nutrūkęs simbolis.
func runeClusters(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var b strings.Builder
	joinNext := false
	pendingRI := false // laukia antrojo regioninio rodiklio (vėliavos pora)
	for _, r := range s {
		if b.Len() == 0 {
			b.WriteRune(r)
			if isRegionalIndicator(r) {
				pendingRI = true
			}
			continue
		}
		if joinNext {
			// Po ZWJ seka vienas simbolis — jis priklauso tam pačiam
			// klasteriui (emoji seka: 👨‍👩‍👧).
			b.WriteRune(r)
			joinNext = false
			continue
		}
		if r == 0x200D { // ZWJ
			b.WriteRune(r)
			joinNext = true
			continue
		}
		if isRegionalIndicator(r) {
			if pendingRI {
				// Antra vėliavos dalis — priklauso tam pačiam klasteriui.
				b.WriteRune(r)
				pendingRI = false
				continue
			}
			// Nauja vėliava — baigiam ankstesnį klasterį.
			out = append(out, b.String())
			b.Reset()
			b.WriteRune(r)
			pendingRI = true
			continue
		}
		if inRanges(r, zeroWidthRanges) || unicode.In(r, unicode.Mn, unicode.Me) {
			// Derinamieji — pridedami prie esamo klasterio.
			// Ypatingai: emoji odų tonai (0x1F3FB–0x1F3FF) — jie turi
			// prilįpti prie emoji, ne formuoti atskirą klasterį
			// (kitaip 👶🏽 būtų 4 ląstelės vietoj 2).
			if r >= 0x1F3FB && r <= 0x1F3FF {
				b.WriteRune(r)
				// Odų tonas baigia klasterį — seka baigiasi.
				out = append(out, b.String())
				b.Reset()
				pendingRI = false
				continue
			}
			b.WriteRune(r)
			continue
		}
		out = append(out, b.String())
		b.Reset()
		b.WriteRune(r)
		pendingRI = false
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// isRegionalIndicator ar r yra regioninis rodiklis (vėliavos simbolis).
func isRegionalIndicator(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF
}

// clusterWidth grąžina grafinio klasterio plotį ląstelėmis.
//
// Ypatingi atvejai:
//   - regioniniai rodikliai (vėliavos) — pora = 2 ląstelės, ne 4;
//   - ZWJ emoji seka (👨‍👩‍👧) — 2 ląstelės, ne 6;
//   - emoji su odų tonu / variantų selektoriumi — 2 ląstelės.
func clusterWidth(cluster string) int {
	if cluster == "" {
		return 0
	}
	hasZWJ := false
	hasRI := false
	riCount := 0
	hasEmoji := false
	hasModifier := false
	sum := 0
	for _, r := range cluster {
		if r == 0x200D {
			hasZWJ = true
			continue
		}
		if r >= 0x1F1E6 && r <= 0x1F1FF {
			hasRI = true
			riCount++
			continue
		}
		if inRanges(r, zeroWidthRanges) && r != 0x200D {
			// Odų tonai ir variantų selektoriai — 0 plotio, bet
			// signalizuoja, kad tai emoji (kuri turi 2 ląsteles).
			if r >= 0x1F3FB && r <= 0x1F3FF {
				hasModifier = true
			}
			continue
		}
		w := runeWidth(r)
		sum += w
		if w == 2 {
			hasEmoji = true
		}
	}
	switch {
	case hasZWJ:
		return 2
	case hasRI:
		// Vėliava: 1 ar 2 regioniniai rodikliai → visada 2 ląstelės.
		return 2
	case hasEmoji && hasModifier:
		// Emoji + odų tonas: 👶🏽 = 2 ląstelės (sum buvo 4).
		return 2
	case hasEmoji && sum > 2:
		return 2
	}
	return sum
}

// clustersWidth grąžina visų klasterių bendrą plotį.
func clustersWidth(clusters []string) int {
	w := 0
	for _, c := range clusters {
		w += clusterWidth(c)
	}
	return w
}

// truncateWidth nukerpa tekstą taip, kad jis tilptų į max ląstelių.
//
// Jei nukirpta — pridedama „…" (1 ląstelė), kad būtų matoma, kad tekstas
// nutrumpintas. Funkcija NEDALINA grafinių klasterių.
func truncateWidth(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if stringWidth(s) <= max {
		return s
	}
	clusters := runeClusters(s)
	// Rezervuojame vietą ellipsės ženklui.
	budget := max - 1
	if budget < 0 {
		budget = 0
	}
	var b strings.Builder
	used := 0
	for _, c := range clusters {
		w := clusterWidth(c)
		if used+w > budget {
			break
		}
		b.WriteString(c)
		used += w
	}
	return b.String() + "…"
}

// padRight prideda tarpus iki nurodyto pločio ląstelėmis.
//
// fmt.Sprintf("%-*s") naudoja RUNE'ų skaičių, o ne ląstelių — su CJK ar
// emoji tekstu stulpeliai pasidaro nelygūs. Ši funkcija skaičiuoja
// ląsteles.
func padRight(s string, width int) string {
	w := stringWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// fitWidth nukerpa tekstą (jei reikia) ir prideda tarpus iki tiksliai
// nurodyto pločio ląstelėmis.
func fitWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return padRight(truncateWidth(s, width), width)
}
