package ast

import (
	"github.com/jensneuse/graphql-go-tools/internal/pkg/unsafebytes"
	"github.com/jensneuse/graphql-go-tools/pkg/lexer/position"
)

// InlineFragment
// example:
// ... on User {
//      friends {
//        count
//      }
//    }
type InlineFragment struct {
	Spread        position.Position // ...
	TypeCondition TypeCondition     // on NamedType, e.g. on User
	HasDirectives bool
	Directives    DirectiveList // optional, e.g. @foo
	SelectionSet  int           // optional, e.g. { nextField }
	HasSelections bool
}

func (d *Document) CopyInlineFragment(ref int) int {
	var directives DirectiveList
	var selectionSet int
	if d.InlineFragments[ref].HasDirectives {
		directives = d.CopyDirectiveList(d.InlineFragments[ref].Directives)
	}
	if d.InlineFragments[ref].HasSelections {
		selectionSet = d.CopySelectionSet(d.InlineFragments[ref].SelectionSet)
	}
	return d.AddInlineFragment(InlineFragment{
		TypeCondition: d.InlineFragments[ref].TypeCondition, // Value type; doesn't need to be copied.
		HasDirectives: d.InlineFragments[ref].HasDirectives,
		Directives:    directives,
		SelectionSet:  selectionSet,
		HasSelections: d.InlineFragments[ref].HasSelections,
	})
}

func (d *Document) InlineFragmentTypeConditionName(ref int) ByteSlice {
	if d.InlineFragments[ref].TypeCondition.Type == -1 {
		return nil
	}
	return d.Input.ByteSlice(d.Types[d.InlineFragments[ref].TypeCondition.Type].Name)
}

func (d *Document) InlineFragmentTypeConditionNameString(ref int) string {
	return unsafebytes.BytesToString(d.InlineFragmentTypeConditionName(ref))
}

func (d *Document) InlineFragmentHasTypeCondition(ref int) bool {
	return d.InlineFragments[ref].TypeCondition.Type != -1
}

func (d *Document) InlineFragmentHasDirectives(ref int) bool {
	return len(d.InlineFragments[ref].Directives.Refs) != 0
}

func (d *Document) InlineFragmentSelections(ref int) []int {
	if !d.InlineFragments[ref].HasSelections {
		return nil
	}
	return d.SelectionSets[d.InlineFragments[ref].SelectionSet].SelectionRefs
}

func (d *Document) AddInlineFragment(fragment InlineFragment) int {
	d.InlineFragments = append(d.InlineFragments, fragment)
	return len(d.InlineFragments) - 1
}

func (d *Document) ExpandInterfaceInlineFragment(inlineFragment int, parentSelectionSet int, concreteTypeNames []string) {
	replacementSelectionSet := d.AddSelectionSet().Ref

	for _, typeName := range concreteTypeNames {
		namedType := d.AddNamedType([]byte(typeName))
		newInlineFragment := d.CopyInlineFragment(inlineFragment)

		d.InlineFragments[newInlineFragment].TypeCondition = TypeCondition{
			Type: namedType,
		}

		d.AddSelection(replacementSelectionSet, Selection{
			Kind: SelectionKindInlineFragment,
			Ref:  newInlineFragment,
		})
	}

	index, _ := d.SelectionIndex(SelectionKindInlineFragment, inlineFragment, parentSelectionSet)

	d.ReplaceSelectionOnSelectionSet(parentSelectionSet, index, replacementSelectionSet)
}

func (d *Document) PromoteUnionInlineFragments(inlineFragment int, parentSelectionSet int) {
	index, _ := d.SelectionIndex(SelectionKindInlineFragment, inlineFragment, parentSelectionSet)

	d.ReplaceSelectionOnSelectionSet(parentSelectionSet, index, d.InlineFragments[inlineFragment].SelectionSet)
}
