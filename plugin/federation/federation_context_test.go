//go:generate go run ../../testdata/gqlgen.go -config testdata/context/gqlgen.yml
package federation

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	fedcontext "github.com/99designs/gqlgen/plugin/federation/testdata/context"
	"github.com/99designs/gqlgen/plugin/federation/testdata/context/generated"
)

// TestFromContext verifies that a field carrying a @fromContext contextual
// argument generates a subgraph that behaves exactly as Apollo Federation 2.8
// requires.
//
// The behaviour is verified against the federation source (v2.14.1):
//   - The Router sends the subgraph an entity fetch in which the contextual
//     argument is an ordinary field argument bound to an operation variable,
//     e.g. `amountInUserCurrency(currencyCode: $contextualArgument_1_0)`, with
//     the value supplied in the request's variables map (computed Router-side
//     via contextRewrites). See query-planner buildPlan / querygraph.
//   - The argument must REMAIN in the subgraph's published SDL (_service.sdl)
//     and on the field, otherwise that Router fetch would fail subgraph-side
//     validation.
//   - The argument is stripped only from the *supergraph* API schema, during
//     composition (merge.ts arg.remove()) — that is Router/composition owned,
//     not the subgraph's job.
//
// So the subgraph's obligations are: define the directives, keep them in the
// served SDL, and accept the value as a normal field argument. No special
// runtime plumbing, no extraction from representations.
func TestFromContext(t *testing.T) {
	srv := handler.New(
		generated.NewExecutableSchema(generated.Config{
			Resolvers: &fedcontext.Resolver{},
		}),
	)
	srv.AddTransport(transport.POST{})
	srv.Use(extension.Introspection{})
	c := client.New(srv)

	representations := []map[string]any{
		{
			"__typename": "Transaction",
			"id":         "txn-1",
		},
	}

	var resp struct {
		Entities []struct {
			ID                   string `json:"id"`
			AmountInUserCurrency string `json:"amountInUserCurrency"`
		} `json:"_entities"`
	}

	// Reproduce the Router's actual entity fetch: the contextual argument is
	// declared as an operation variable and applied to the field, exactly as
	// the query planner emits `field(arg: $contextualArgument_N_M)`.
	routerFetch := `query($representations:[_Any!]!, $contextualArgument_1_0: String) {
		_entities(representations:$representations) {
			... on Transaction { id amountInUserCurrency(currencyCode: $contextualArgument_1_0) }
		}
	}`

	err := c.Post(
		routerFetch,
		&resp,
		client.Var("representations", representations),
		client.Var("contextualArgument_1_0", "EUR"),
	)

	require.NoError(t, err)
	require.Len(t, resp.Entities, 1)
	require.Equal(t, "txn-1", resp.Entities[0].ID)
	require.Equal(t, "100 EUR", resp.Entities[0].AmountInUserCurrency)

	// The @context / @fromContext usages must round-trip into the _service SDL
	// so rover/the Router can compose the supergraph and build the query plan.
	var sdlResp struct {
		Service struct {
			SDL string `json:"sdl"`
		} `json:"_service"`
	}
	require.NoError(t, c.Post(`{_service{sdl}}`, &sdlResp))
	require.Contains(t, sdlResp.Service.SDL, `@context(name: "userContext")`)
	require.Contains(t, sdlResp.Service.SDL, `@fromContext(field: "$userContext { userCurrency }")`)
}
