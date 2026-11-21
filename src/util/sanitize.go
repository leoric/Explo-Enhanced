package util

import (
	"regexp"
	"strings"
)

var (
	filenameRe     = regexp.MustCompile(`[^\p{L}\d._,\-]+`)
	alnumRe        = regexp.MustCompile(`[^\p{L}\d]+`)
	featTailRe     = regexp.MustCompile(`(?i)\s*[\(\[\{]\s*(feat\.?|featuring|ft\.?|with)\s[^\)\]\}]*[\)\]\}]\s*$`)
	remasterTailRe = regexp.MustCompile(`(?i)\s*(?:[-–—]|\(|\[)\s*(?:\d{4}\s+|digital\s+)?remaster(ed)?\)?\]?\s*$`)
	// Indian film-song tags: (From "Film"), [Original Motion Picture Soundtrack], (Tamil), - Telugu
	fromTagRe     = regexp.MustCompile(`(?i)\s*[\(\[]\s*from\s*[-:]?\s*(?:"[^"]*"|“[^”]*”|[^\)\]]*)\s*[\)\]]`)
	soundtrackRe  = regexp.MustCompile(`(?i)\s*[\(\[]\s*original\s+(?:motion\s+picture\s+soundtrack|background\s+score|score|soundtrack)\s*[\)\]]`)
	languageTagRe = regexp.MustCompile(`(?i)\s*(?:[\(\[]\s*(tamil|telugu|kannada|malayalam|hindi)\s*[\)\]]|[-–]\s*(tamil|telugu|kannada|malayalam|hindi)\b)`)
)

// stripTags removes feat./remaster tails and film-song tags that a music library usually doesn't have.
func stripTags(s string) string {
	s = fromTagRe.ReplaceAllString(s, "")
	s = soundtrackRe.ReplaceAllString(s, "")
	s = languageTagRe.ReplaceAllString(s, "")
	s = featTailRe.ReplaceAllString(s, "")
	return remasterTailRe.ReplaceAllString(s, "")
}

// CleanSearchTitle strips trailing (feat. …), "- 2011 Remaster" and film-song tags
// but keeps the title human-readable for use in search API queries.
func CleanSearchTitle(s string) string {
	return strings.TrimSpace(stripTags(s))
}

// NormalizeTitle strips the same tags as CleanSearchTitle, lowercases,
// and reduces to alphanumeric-only for fuzzy title comparison.
func NormalizeTitle(s string) string {
	return AlnumOnly(strings.ToLower(stripTags(s)))
}

// Language returns the language tag ("tamil", "telugu", …) found in any of the given strings,
// e.g. "Hukum - Telugu" or "KGF Chapter 1 (Tamil)", or "" when none says so.
func Language(texts ...string) string {
	for _, t := range texts {
		if m := languageTagRe.FindStringSubmatch(t); m != nil {
			return strings.ToLower(m[1] + m[2])
		}
	}
	return ""
}

// FilenameSafe replaces characters unsafe for filenames with '_'
func FilenameSafe(s string) string {
	return filenameRe.ReplaceAllString(s, "_")
}

// AlnumOnly removes everything except letters and digits
func AlnumOnly(s string) string {
	return alnumRe.ReplaceAllString(s, "")
}

// Case insensitive check if substring is present in s
func ContainsFold(s, substr string) bool {
    return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}