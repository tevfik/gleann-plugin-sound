//go:build onnx
// +build onnx

package onnx

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// tokenizerJSON mirrors the structure of Hugging Face tokenizer.json
// used by whisper ONNX exports.
type tokenizerJSON struct {
	Model struct {
		Vocab map[string]int `json:"vocab"`
	} `json:"model"`
	AddedTokens []struct {
		ID      int    `json:"id"`
		Content string `json:"content"`
	} `json:"added_tokens"`
}

// loadTokenizer reads a tokenizer.json file and returns an ordered vocabulary
// slice where index = token ID.
func loadTokenizer(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tokenizer: %w", err)
	}

	var tok tokenizerJSON
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("parse tokenizer: %w", err)
	}

	// Determine max token ID.
	maxID := 0
	for _, id := range tok.Model.Vocab {
		if id > maxID {
			maxID = id
		}
	}
	for _, t := range tok.AddedTokens {
		if t.ID > maxID {
			maxID = t.ID
		}
	}

	vocab := make([]string, maxID+1)

	// Fill from model vocab.
	for token, id := range tok.Model.Vocab {
		// Whisper uses GPT-2 byte-level BPE.
		// The tokenizer.json stores tokens with Unicode replacements;
		// decode the common Ġ → space mapping.
		decoded := strings.ReplaceAll(token, "Ġ", " ")
		decoded = strings.ReplaceAll(decoded, "Ċ", "\n")
		vocab[id] = decoded
	}

	// Override with added tokens (special tokens).
	for _, t := range tok.AddedTokens {
		vocab[t.ID] = t.Content
	}

	// Sanity check.
	nonEmpty := 0
	for _, v := range vocab {
		if v != "" {
			nonEmpty++
		}
	}
	if nonEmpty < 100 {
		return nil, fmt.Errorf("tokenizer seems broken: only %d non-empty tokens", nonEmpty)
	}

	return vocab, nil
}

// dumpVocab is a debug helper that writes vocab to stdout, sorted by ID.
func dumpVocab(vocab []string) {
	type entry struct {
		id    int
		token string
	}
	var entries []entry
	for i, v := range vocab {
		if v != "" {
			entries = append(entries, entry{i, v})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].id < entries[j].id })
	for _, e := range entries {
		fmt.Printf("%5d  %q\n", e.id, e.token)
	}
}
