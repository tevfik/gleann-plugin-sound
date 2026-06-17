package evaluate

import (
	"strings"
	"unicode"
)

// CalculateWER calculates Word Error Rate (WER) using Levenshtein distance.
// WER = (Substitutions + Deletions + Insertions) / Total Words in Reference
func CalculateWER(reference, hypothesis string) float64 {
	refWords := splitWords(strings.ToLower(reference))
	hypWords := splitWords(strings.ToLower(hypothesis))

	if len(refWords) == 0 {
		if len(hypWords) == 0 {
			return 0.0
		}
		return 1.0
	}

	dist := levenshteinDistance(refWords, hypWords)
	return float64(dist) / float64(len(refWords))
}

// CalculateCER calculates Character Error Rate (CER) using Levenshtein distance.
// CER = (Substitutions + Deletions + Insertions) / Total Characters in Reference
func CalculateCER(reference, hypothesis string) float64 {
	refRunes := []rune(strings.ToLower(reference))
	hypRunes := []rune(strings.ToLower(hypothesis))

	// Remove spaces and punctuation for a more accurate character error rate on actual spoken content
	refClean := filterAlphanumeric(refRunes)
	hypClean := filterAlphanumeric(hypRunes)

	if len(refClean) == 0 {
		if len(hypClean) == 0 {
			return 0.0
		}
		return 1.0
	}

	dist := levenshteinDistanceRunes(refClean, hypClean)
	return float64(dist) / float64(len(refClean))
}

func splitWords(s string) []string {
	var words []string
	fields := strings.Fields(s)
	for _, f := range fields {
		clean := strings.TrimFunc(f, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		})
		if clean != "" {
			words = append(words, clean)
		}
	}
	return words
}

func filterAlphanumeric(runes []rune) []rune {
	var res []rune
	for _, r := range runes {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			res = append(res, r)
		}
	}
	return res
}

func levenshteinDistance(a, b []string) int {
	lenA := len(a)
	lenB := len(b)

	dp := make([][]int, lenA+1)
	for i := range dp {
		dp[i] = make([]int, lenB+1)
		dp[i][0] = i
	}
	for j := 0; j <= lenB; j++ {
		dp[0][j] = j
	}

	for i := 1; i <= lenA; i++ {
		for j := 1; j <= lenB; j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			dp[i][j] = min(
				dp[i-1][j]+1,      // Deletion
				dp[i][j-1]+1,      // Insertion
				dp[i-1][j-1]+cost, // Substitution
			)
		}
	}

	return dp[lenA][lenB]
}

func levenshteinDistanceRunes(a, b []rune) int {
	lenA := len(a)
	lenB := len(b)

	dp := make([][]int, lenA+1)
	for i := range dp {
		dp[i] = make([]int, lenB+1)
		dp[i][0] = i
	}
	for j := 0; j <= lenB; j++ {
		dp[0][j] = j
	}

	for i := 1; i <= lenA; i++ {
		for j := 1; j <= lenB; j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			dp[i][j] = min(
				dp[i-1][j]+1,      // Deletion
				dp[i][j-1]+1,      // Insertion
				dp[i-1][j-1]+cost, // Substitution
			)
		}
	}

	return dp[lenA][lenB]
}

func min(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
