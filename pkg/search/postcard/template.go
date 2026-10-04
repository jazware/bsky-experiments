package postcard

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
)

//go:embed card.html
var cardTemplateSrc string

var cardTemplate = template.Must(template.New("card").Funcs(template.FuncMap{
	"compact": compactCount,
	"dict":    templateDict,
}).Parse(cardTemplateSrc))

// compactCount formats counts the way the Bluesky client does (1.2K, 3.4M)
func compactCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1_000_000)) + "M"
	case n >= 1_000:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/1_000)) + "K"
	default:
		return fmt.Sprintf("%d", n)
	}
}

func trimZero(s string) string {
	if len(s) > 2 && s[len(s)-2:] == ".0" {
		return s[:len(s)-2]
	}
	return s
}

func templateDict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict requires an even number of arguments")
	}
	d := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict keys must be strings")
		}
		d[key] = pairs[i+1]
	}
	return d, nil
}

// RenderHTML renders the card display model to a standalone HTML document
func RenderHTML(card *Card) (string, error) {
	var buf bytes.Buffer
	if err := cardTemplate.Execute(&buf, card); err != nil {
		return "", fmt.Errorf("failed to render card template: %w", err)
	}
	return buf.String(), nil
}
