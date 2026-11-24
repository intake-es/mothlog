package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/intake-es/mothlog/internal/lang"
)

// Una herramienta nueva que cambie algo tiene que ir en writeTools, o un
// token con solo read podría usarla.
func TestToolScopesMatchAnnotations(t *testing.T) {
	ctx := context.Background()
	server := New(nil, Options{})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	for _, tool := range res.Tools {
		readOnly := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		if readOnly == (ToolScope(tool.Name) == ScopeWrite) {
			t.Errorf("%s: readOnlyHint %v y permiso %s", tool.Name, readOnly, ToolScope(tool.Name))
		}
		if ToolScope(tool.Name) == ScopeWrite {
			writes++
		}
	}
	if writes != len(writeTools) {
		t.Errorf("%d herramientas de escribir, y writeTools tiene %d", writes, len(writeTools))
	}
}

// En español, cada herramienta lleva su descripción y la de sus campos en
// español; si falta una, New entra en pánico.
func TestSpanishDescriptions(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		l    lang.Lang
		want string
	}{{lang.EN, "Errors from the applications that send to Mothlog"}, {lang.ES, "Errores de las aplicaciones que envían a Mothlog"}} {
		server := New(nil, Options{Lang: tc.l})
		st, ct := mcp.NewInMemoryTransports()
		if _, err := server.Connect(ctx, st, nil); err != nil {
			t.Fatal(err)
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := cs.InitializeResult().Instructions; !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: instrucciones = %.60q", tc.l, got)
		}
		if name := cs.InitializeResult().ServerInfo.Name; name != "mothlog" {
			t.Errorf("nombre del servidor = %q", name)
		}
		res, err := cs.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range res.Tools {
			b, _ := json.Marshal(tool.InputSchema)
			spanish := strings.Contains(tool.Description, "Úsala") || strings.Contains(tool.Description, "Devuelve")
			if spanish != (tc.l == lang.ES) {
				t.Errorf("%s: %s: %q", tc.l, tool.Name, tool.Description)
			}
			if tc.l == lang.EN && strings.Contains(string(b), "vacío") || tc.l == lang.ES && strings.Contains(string(b), "empty") {
				t.Errorf("%s: %s: esquema en el otro idioma: %s", tc.l, tool.Name, b)
			}
		}
		_ = cs.Close()
	}
}
