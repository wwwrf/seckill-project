package rag

import (
	"bufio"
	"math"
	"os"
	"sort"
	"strings"
	"unicode"
)

type Chunk struct {
	ID     int
	Title  string
	Text   string
	Vector []float64
	Source string
}

type Hit struct {
	Title  string  `json:"title"`
	Source string  `json:"source"`
	Score  float64 `json:"score"`
	Text   string  `json:"text"`
}

type Index struct {
	dim    int
	chunks []Chunk
}

func NewIndex(dim int) *Index {
	if dim <= 0 {
		dim = 128
	}
	return &Index{dim: dim, chunks: make([]Chunk, 0)}
}

func (idx *Index) BuildFromMarkdown(path string, chunkSize int) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	if chunkSize <= 0 {
		chunkSize = 420
	}

	scanner := bufio.NewScanner(file)
	title := "General"
	var paragraph strings.Builder
	id := 1

	flushParagraph := func() {
		raw := strings.TrimSpace(paragraph.String())
		paragraph.Reset()
		if raw == "" {
			return
		}

		// Split long paragraphs into fixed-size chunks.
		for len(raw) > 0 {
			part := raw
			if len(part) > chunkSize {
				part = raw[:chunkSize]
				raw = strings.TrimSpace(raw[chunkSize:])
			} else {
				raw = ""
			}

			c := Chunk{
				ID:     id,
				Title:  title,
				Text:   part,
				Vector: embed(part, idx.dim),
				Source: path,
			}
			idx.chunks = append(idx.chunks, c)
			id++
		}
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			flushParagraph()
			title = strings.TrimSpace(strings.TrimLeft(line, "#"))
			if title == "" {
				title = "General"
			}
			continue
		}
		if line == "" {
			flushParagraph()
			continue
		}

		if paragraph.Len() > 0 {
			paragraph.WriteString(" ")
		}
		paragraph.WriteString(line)
	}

	flushParagraph()
	return scanner.Err()
}

func (idx *Index) Retrieve(query string, topK int) []Hit {
	if topK <= 0 {
		topK = 3
	}
	if len(idx.chunks) == 0 {
		return nil
	}

	qVec := embed(query, idx.dim)
	type scored struct {
		chunk Chunk
		score float64
	}
	all := make([]scored, 0, len(idx.chunks))

	for _, c := range idx.chunks {
		all = append(all, scored{chunk: c, score: cosine(qVec, c.Vector)})
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].score > all[j].score
	})

	if topK > len(all) {
		topK = len(all)
	}

	hits := make([]Hit, 0, topK)
	for i := 0; i < topK; i++ {
		hits = append(hits, Hit{
			Title:  all[i].chunk.Title,
			Source: all[i].chunk.Source,
			Score:  all[i].score,
			Text:   all[i].chunk.Text,
		})
	}
	return hits
}

func embed(text string, dim int) []float64 {
	vec := make([]float64, dim)
	tokens := tokenize(text)
	if len(tokens) == 0 {
		return vec
	}

	for _, t := range tokens {
		h := hashToken(t) % uint64(dim)
		vec[int(h)] += 1.0
	}

	// L2 normalize.
	var norm float64
	for _, v := range vec {
		norm += v * v
	}
	if norm == 0 {
		return vec
	}
	norm = math.Sqrt(norm)
	for i := range vec {
		vec[i] = vec[i] / norm
	}

	return vec
}

func cosine(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

func tokenize(text string) []string {
	text = strings.ToLower(text)
	tokens := make([]string, 0, len(text))
	var word strings.Builder

	flush := func() {
		if word.Len() > 0 {
			tokens = append(tokens, word.String())
			word.Reset()
		}
	}

	for _, r := range text {
		if isHan(r) {
			flush()
			tokens = append(tokens, string(r))
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

func isHan(r rune) bool {
	return r >= 0x4E00 && r <= 0x9FFF
}

func hashToken(s string) uint64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
