package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
