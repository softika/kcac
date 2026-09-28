package resolve

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/softika/kcac/internal/model"
)

// reach is one role reachable from an origin, with the chain that got there.
type reach struct {
	Role  model.RoleKey
	Chain []model.RoleKey
}

// expander walks the composite role graph.
//
// Holding a composite role means holding that role AND everything it contains,
// recursively, so the closure includes the origin itself. Traversal is
// breadth-first, which yields the SHORTEST chain to each reachable role and
// cannot recurse into a cycle.
type expander struct {
	edges  map[model.RoleKey][]model.RoleKey
	sorted map[model.RoleKey][]model.RoleKey
	memo   map[model.RoleKey][]reach
	cycles map[string]struct{}
}

func newExpander(edges map[model.RoleKey][]model.RoleKey) *expander {
	return &expander{
		edges:  edges,
		sorted: make(map[model.RoleKey][]model.RoleKey, len(edges)),
		memo:   make(map[model.RoleKey][]reach, len(edges)),
		cycles: make(map[string]struct{}),
	}
}

// children returns a role's direct composites in a stable order, so two runs
// over the same realm produce identical output.
func (e *expander) children(key model.RoleKey) []model.RoleKey {
	if cached, ok := e.sorted[key]; ok {
		return cached
	}
	kids := slices.Clone(e.edges[key])
	slices.SortFunc(kids, func(a, b model.RoleKey) int { return cmp.Compare(a.String(), b.String()) })
	e.sorted[key] = kids
	return kids
}

// closure returns every role held by virtue of holding origin, including origin
// itself, each with the shortest chain from origin.
func (e *expander) closure(origin model.RoleKey) []reach {
	if cached, ok := e.memo[origin]; ok {
		return cached
	}

	type node struct {
		key   model.RoleKey
		chain []model.RoleKey
	}

	visited := map[model.RoleKey]bool{origin: true}
	queue := []node{{key: origin, chain: []model.RoleKey{origin}}}
	out := make([]reach, 0, 1+len(e.edges[origin]))

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		out = append(out, reach{Role: cur.key, Chain: cur.chain})

		for _, child := range e.children(cur.key) {
			// A child already on this chain is a genuine cycle. Record it and
			// stop that branch: a malformed graph must not hang a collection, and
			// truncating silently would hide it.
			if slices.Contains(cur.chain, child) {
				e.recordCycle(cur.chain, child)
				continue
			}
			// Already reached by an equal or shorter route: a diamond, not a
			// cycle, and nothing to report.
			if visited[child] {
				continue
			}
			visited[child] = true
			queue = append(queue, node{
				key:   child,
				chain: append(slices.Clone(cur.chain), child),
			})
		}
	}

	e.memo[origin] = out
	return out
}

func (e *expander) recordCycle(chain []model.RoleKey, repeat model.RoleKey) {
	e.cycles[fmt.Sprintf("composite role cycle detected: %s → %s (expansion stopped at the repeat)",
		joinRoles(chain), displayRole(repeat))] = struct{}{}
}

// warnings returns any cycles found, sorted for stable output.
func (e *expander) warnings() []string {
	out := make([]string, 0, len(e.cycles))
	for w := range e.cycles {
		out = append(out, w)
	}
	slices.Sort(out)
	return out
}
