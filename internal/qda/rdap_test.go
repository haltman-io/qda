package qda

import (
	"encoding/json"
	"testing"
)

func TestBootstrapFindBaseUsesLongestCandidate(t *testing.T) {
	bootstrap := &RDAPBootstrap{
		Services: [][][]string{
			{{"br"}, {"https://rdap.registro.br/"}},
			{{"com"}, {"https://rdap.example.com/"}},
		},
	}
	if got := bootstrap.FindBase("example.com.br"); got != "https://rdap.registro.br/" {
		t.Fatalf("got %q", got)
	}
}

func TestFindRegistrarFromVCard(t *testing.T) {
	raw := json.RawMessage(`["vcard",[["version",{},"text","4.0"],["fn",{},"text","Example Registrar"]]]`)
	entity := rdapEntity{Handle: "9999", Roles: []string{"registrar"}, VCardArray: raw}
	got := findRegistrar([]rdapEntity{entity})
	if got != "Example Registrar (9999)" {
		t.Fatalf("got %q", got)
	}
}
