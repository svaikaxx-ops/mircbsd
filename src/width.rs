//! Ekrano pločio matavimas (port from width.go).

use unicode_general_category::{get_general_category, GeneralCategory};

/// Platūs (2 ląstelės) intervalai — East Asian Wide + Fullwidth + emoji.
pub const WIDE_RANGES: &[(u32, u32)] = &[
    (0x1100, 0x115F),
    (0x231A, 0x231B),
    (0x2329, 0x232A),
    (0x23E9, 0x23EC),
    (0x23F0, 0x23F0),
    (0x23F3, 0x23F3),
    (0x25FD, 0x25FE),
    (0x2614, 0x2615),
    (0x2648, 0x2653),
    (0x267F, 0x267F),
    (0x2693, 0x2693),
    (0x26A1, 0x26A1),
    (0x26AA, 0x26AB),
    (0x26BD, 0x26BE),
    (0x26C4, 0x26C5),
    (0x26CE, 0x26CE),
    (0x26D4, 0x26D4),
    (0x26EA, 0x26EA),
    (0x26F2, 0x26F3),
    (0x26F5, 0x26F5),
    (0x26FA, 0x26FA),
    (0x26FD, 0x26FD),
    (0x2705, 0x2705),
    (0x270A, 0x270B),
    (0x2728, 0x2728),
    (0x274C, 0x274C),
    (0x274E, 0x274E),
    (0x2753, 0x2755),
    (0x2757, 0x2757),
    (0x2795, 0x2797),
    (0x27B0, 0x27B0),
    (0x27BF, 0x27BF),
    (0x2B1B, 0x2B1C),
    (0x2B50, 0x2B50),
    (0x2B55, 0x2B55),
    (0x2E80, 0x2E99),
    (0x2E9B, 0x2EF3),
    (0x2F00, 0x2FD5),
    (0x2FF0, 0x2FFB),
    (0x3000, 0x303E),
    (0x3041, 0x3096),
    (0x309A, 0x30FF),
    (0x3105, 0x312F),
    (0x3131, 0x318E),
    (0x3190, 0x31E3),
    (0x31F0, 0x321E),
    (0x3220, 0x3247),
    (0x3250, 0x4DBF),
    (0x4E00, 0xA4CF),
    (0xA960, 0xA97C),
    (0xAC00, 0xD7A3),
    (0xF900, 0xFAFF),
    (0xFE10, 0xFE19),
    (0xFE30, 0xFE52),
    (0xFE54, 0xFE66),
    (0xFE68, 0xFE6B),
    (0xFF01, 0xFF60),
    (0xFFE0, 0xFFE6),
    (0x16FE0, 0x16FE4),
    (0x17000, 0x18AFF),
    (0x1B000, 0x1B2FF),
    (0x1F004, 0x1F004),
    (0x1F0CF, 0x1F0CF),
    (0x1F18E, 0x1F18E),
    (0x1F191, 0x1F19A),
    (0x1F1E6, 0x1F1FF),
    (0x1F200, 0x1F320),
    (0x1F32D, 0x1F335),
    (0x1F337, 0x1F37C),
    (0x1F37E, 0x1F393),
    (0x1F3A0, 0x1F3CA),
    (0x1F3CF, 0x1F3D3),
    (0x1F3E0, 0x1F3F0),
    (0x1F3F4, 0x1F3F4),
    (0x1F3F8, 0x1F43E),
    (0x1F440, 0x1F440),
    (0x1F442, 0x1F4FC),
    (0x1F4FF, 0x1F53D),
    (0x1F54B, 0x1F54E),
    (0x1F550, 0x1F567),
    (0x1F57A, 0x1F57A),
    (0x1F595, 0x1F596),
    (0x1F5A4, 0x1F5A4),
    (0x1F5FB, 0x1F64F),
    (0x1F680, 0x1F6C5),
    (0x1F6CC, 0x1F6CC),
    (0x1F6D0, 0x1F6D2),
    (0x1F6D5, 0x1F6D7),
    (0x1F6DC, 0x1F6DF),
    (0x1F6EB, 0x1F6EC),
    (0x1F6F4, 0x1F6FC),
    (0x1F7E0, 0x1F7EB),
    (0x1F7F0, 0x1F7F0),
    (0x1F90C, 0x1F93A),
    (0x1F93C, 0x1F945),
    (0x1F947, 0x1F9FF),
    (0x1FA70, 0x1FAFF),
    (0x20000, 0x2FFFD),
    (0x30000, 0x3FFFD),
];

/// Intervalai, kurie nematomi (derinamieji ženklai).
pub const ZERO_WIDTH_RANGES: &[(u32, u32)] = &[
    (0x0300, 0x036F),
    (0x0483, 0x0489),
    (0x0591, 0x05BD),
    (0x0610, 0x061A),
    (0x064B, 0x065F),
    (0x0670, 0x0670),
    (0x06D6, 0x06DC),
    (0x0730, 0x074A),
    (0x07A6, 0x07B0),
    (0x0900, 0x0903),
    (0x093A, 0x093C),
    (0x0941, 0x0948),
    (0x094D, 0x094D),
    (0x0951, 0x0957),
    (0x0962, 0x0963),
    (0x0E31, 0x0E31),
    (0x0E34, 0x0E3A),
    (0x0E47, 0x0E4E),
    (0x1AB0, 0x1AFF),
    (0x1DC0, 0x1DFF),
    (0x200B, 0x200F),
    (0x2028, 0x202E),
    (0x2060, 0x2064),
    (0x20D0, 0x20F0),
    (0x3099, 0x3099),
    (0xFE00, 0xFE0F),
    (0xFE20, 0xFE2F),
    (0xFEFF, 0xFEFF),
    (0x1F3FB, 0x1F3FF),
    (0xE0100, 0xE01EF),
];

fn is_mark(c: char) -> bool {
    matches!(
        get_general_category(c),
        GeneralCategory::NonspacingMark | GeneralCategory::EnclosingMark
    )
}

/// Patikrina, ar `r` patenka į kurį nors intervalą (dvejetainė paieška).
pub fn in_ranges(r: char, ranges: &[(u32, u32)]) -> bool {
    if ranges.is_empty() {
        return false;
    }
    let r = r as u32;
    let mut lo = 0usize;
    let mut hi = ranges.len() - 1;
    while lo <= hi {
        let mid = (lo + hi) / 2;
        let (start, end) = ranges[mid];
        if r < start {
            if mid == 0 {
                break;
            }
            hi = mid - 1;
        } else if r > end {
            lo = mid + 1;
        } else {
            return true;
        }
    }
    false
}

/// Simbolio plotis terminalo ląstelėmis.
pub fn rune_width(r: char) -> i32 {
    if r == '\0' {
        return 0;
    }
    let cp = r as u32;
    if cp < 32 || (0x7F..0xA0).contains(&cp) {
        return 0;
    }
    if cp < 0x0300 {
        return 1;
    }
    if in_ranges(r, ZERO_WIDTH_RANGES) {
        return 0;
    }
    if is_mark(r) {
        return 0;
    }
    if in_ranges(r, WIDE_RANGES) {
        return 2;
    }
    1
}

/// Teksto plotis terminalo ląstelėmis (grafinių klasterių suma).
pub fn string_width(s: &str) -> i32 {
    clusters_width(&rune_clusters(s))
}

/// Tekstas suskaidytas į grafinius klasterius.
pub fn rune_clusters(s: &str) -> Vec<String> {
    if s.is_empty() {
        return Vec::new();
    }
    let mut out: Vec<String> = Vec::new();
    let mut buf = String::new();
    let mut join_next = false;
    let mut pending_ri = false;

    for r in s.chars() {
        if buf.is_empty() {
            buf.push(r);
            if is_regional_indicator(r) {
                pending_ri = true;
            }
            continue;
        }
        if join_next {
            buf.push(r);
            join_next = false;
            continue;
        }
        if r == '\u{200D}' {
            buf.push(r);
            join_next = true;
            continue;
        }
        if is_regional_indicator(r) {
            if pending_ri {
                buf.push(r);
                pending_ri = false;
                continue;
            }
            out.push(std::mem::take(&mut buf));
            buf.push(r);
            pending_ri = true;
            continue;
        }
        if in_ranges(r, ZERO_WIDTH_RANGES) || is_mark(r) {
            let cp = r as u32;
            if (0x1F3FB..=0x1F3FF).contains(&cp) {
                buf.push(r);
                out.push(std::mem::take(&mut buf));
                pending_ri = false;
                continue;
            }
            buf.push(r);
            continue;
        }
        out.push(std::mem::take(&mut buf));
        buf.push(r);
        pending_ri = false;
    }
    if !buf.is_empty() {
        out.push(buf);
    }
    out
}

/// Ar `r` yra regioninis rodiklis (vėliavos simbolis).
pub fn is_regional_indicator(r: char) -> bool {
    let cp = r as u32;
    (0x1F1E6..=0x1F1FF).contains(&cp)
}

/// Apytikslis Unicode `Extended_Pictographic` (emoji ir piktogramos).
fn is_pictographic(r: char) -> bool {
    matches!(
        r as u32,
        0x00A9
            | 0x00AE
            | 0x203C
            | 0x2049
            | 0x2122
            | 0x2139
            | 0x2194..=0x21AA
            | 0x2300..=0x23FF
            | 0x24C2
            | 0x25A0..=0x27BF
            | 0x2934..=0x2935
            | 0x2B05..=0x2B55
            | 0x3030
            | 0x303D
            | 0x3297
            | 0x3299
            | 0x1F000..=0x1FAFF
    )
}

/// Grafinio klasterio plotis ląstelėmis.
pub fn cluster_width(cluster: &str) -> i32 {
    if cluster.is_empty() {
        return 0;
    }
    let mut has_zwj = false;
    let mut has_ri = false;
    let mut _ri_count = 0i32;
    let mut has_emoji = false;
    let mut has_modifier = false;
    let mut all_pictographic = true;
    let mut sum = 0i32;

    for r in cluster.chars() {
        if r == '\u{200D}' {
            has_zwj = true;
            continue;
        }
        let cp = r as u32;
        if (0x1F1E6..=0x1F1FF).contains(&cp) {
            has_ri = true;
            _ri_count += 1;
            continue;
        }
        if in_ranges(r, ZERO_WIDTH_RANGES) && r != '\u{200D}' {
            if (0x1F3FB..=0x1F3FF).contains(&cp) {
                has_modifier = true;
            }
            continue;
        }
        let w = rune_width(r);
        sum += w;
        if w == 2 {
            has_emoji = true;
        }
        if w > 0 && !is_pictographic(r) {
            all_pictographic = false;
        }
    }

    if has_zwj {
        // Į vieną 2 ląstelių glifą terminalai jungia tik emoji. „a‍中“ ar
        // „文‍ “ rodomi kaip atskiri simboliai — skaičiuojam sumą, kitaip
        // eilutė ekrane būtų platesnė nei apskaičiuota (perpildymas).
        return if all_pictographic && sum > 0 { 2 } else { sum };
    }
    if has_ri {
        return 2;
    }
    if has_emoji && has_modifier {
        return 2;
    }
    if has_emoji && sum > 2 {
        return 2;
    }
    sum
}

/// Visų klasterių bendras plotis.
pub fn clusters_width(clusters: &[String]) -> i32 {
    clusters.iter().map(|c| cluster_width(c)).sum()
}

/// Nukerpa tekstą iki `max` ląstelių; prideda „…" jei nukirpta.
pub fn truncate_width(s: &str, max: i32) -> String {
    if max <= 0 {
        return String::new();
    }
    if string_width(s) <= max {
        return s.to_string();
    }
    let clusters = rune_clusters(s);
    let mut budget = max - 1;
    if budget < 0 {
        budget = 0;
    }
    let mut used = 0i32;
    let mut b = String::new();
    for c in &clusters {
        let w = cluster_width(c);
        if used + w > budget {
            break;
        }
        b.push_str(c);
        used += w;
    }
    b.push('…');
    b
}

/// Prideda tarpus dešinėje iki nurodyto pločio ląstelėmis.
pub fn pad_right(s: &str, width: i32) -> String {
    let w = string_width(s);
    if w >= width {
        return s.to_string();
    }
    let pad = (width - w) as usize;
    let mut out = String::with_capacity(s.len() + pad);
    out.push_str(s);
    out.extend(std::iter::repeat_n(' ', pad));
    out
}

/// Nukerpa (jei reikia) ir užpildo iki tiksliai `width` ląstelių.
pub fn fit_width(s: &str, width: i32) -> String {
    if width <= 0 {
        return String::new();
    }
    pad_right(&truncate_width(s, width), width)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rune_width_ascii_and_cjk() {
        assert_eq!(rune_width('a'), 1);
        assert_eq!(rune_width('Z'), 1);
        assert_eq!(rune_width('日'), 2);
        assert_eq!(rune_width('😀'), 2);
        assert_eq!(rune_width('\u{0301}'), 0); // combining acute
    }

    #[test]
    fn string_width_samples() {
        assert_eq!(string_width(""), 0);
        assert_eq!(string_width("abc"), 3);
        assert_eq!(string_width("日本語"), 6);
        assert_eq!(string_width("👨\u{200D}👩\u{200D}👧"), 2);
        assert_eq!(string_width("🇦🇹"), 2);
    }

    #[test]
    fn truncate_width_respects_clusters() {
        let s = "hello世界";
        let got = truncate_width(s, 6);
        assert!(string_width(&got) <= 6);
        assert!(got.contains('…') || string_width(s) <= 6);
    }

    #[test]
    fn wide_and_zero_ranges_sorted() {
        for name in ["WIDE_RANGES", "ZERO_WIDTH_RANGES"] {
            let ranges = if name == "WIDE_RANGES" {
                WIDE_RANGES
            } else {
                ZERO_WIDTH_RANGES
            };
            for i in 1..ranges.len() {
                assert!(
                    ranges[i].0 > ranges[i - 1].1,
                    "{name}: overlap at index {i}"
                );
            }
        }
    }
}

#[cfg(test)]
#[path = "width_tests.rs"]
mod more_tests;
