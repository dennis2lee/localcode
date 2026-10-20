package provider

import (
	"sort"
	"strings"
)

// MinSecretLen is the shortest secret that is hidden. Shorter, and hiding
// it rewrites ordinary words while protecting nothing a guess would not
// find.
const MinSecretLen = 8

// MinEchoLen is the shortest run of a secret's characters taken out of a
// text: long enough that ordinary text does not match it by chance, short
// enough that what is left of a secret cut off mid-way is no use to
// anyone.
const MinEchoLen = 12

// RedactEcho replaces with mark every run of MinEchoLen or more characters
// of text that also appears, in that order, in secret: the whole secret,
// one of a JWT's three parts, or the part of one that survived an
// endpoint's reply being cut short before it got here. Matching the secret
// whole missed all but the first of those. A secret shorter than
// MinSecretLen is not hidden.
func RedactEcho(text, secret, mark string) string {
	secret = strings.TrimSpace(secret)
	if len(secret) < MinSecretLen {
		return text
	}
	least := min(MinEchoLen, len(secret))
	var b strings.Builder
	for i := 0; i < len(text); {
		j := i
		for j < len(text) && strings.Contains(secret, text[i:j+1]) {
			j++
		}
		if j-i >= least {
			b.WriteString(mark)
			i = j
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// RedactSecrets takes this provider's api_key and the values of its
// headers out of text an endpoint wrote, which can quote what it was sent:
// "[the key]" for the key, "[the <name> value]" for a header.
func (p *OpenAICompat) RedactSecrets(text string) string {
	text = RedactEcho(text, p.APIKey, "[the key]")
	names := make([]string, 0, len(p.Headers))
	for name := range p.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		text = RedactEcho(text, p.Headers[name], "[the "+name+" value]")
	}
	return text
}
