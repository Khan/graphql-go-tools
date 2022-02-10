// Package asttransform contains a set of helper methods to make recursive ast transformations possible.
//
// This is especially useful for ast normalization for nested fragment inlining.
//
// This packages is necessary to make AST transformations possible while walking an AST recusively.
// In order to resolve dependencies in a tree (inline fragments & fragment spreads) it's necessary to resolve them in a specific order.
// The right order to not mess things up is from the deepest level up to the root.
// Therefore this package is used to register transformations while walking an AST in order to bring all transformations in the right order.
// Only then, when all transformations are in the right order according to depth, it's possible to safely apply them.
//
package asttransform

import (
	"sort"

	"github.com/jensneuse/graphql-go-tools/pkg/ast"
)

type (
	// Transformable defines the interface which needs to be implemented in order to apply Transformations
	// This needs to be implemented by any AST in order to be transformable
	Transformable interface {
		// DeleteRootNode marks a Node for deletion
		DeleteRootNode(node ast.Node)
		// EmptySelectionSet marks a selectionset for emptying
		EmptySelectionSet(ref int)
		// AppendSelectionSet marks to append a reference to a selectionset
		AppendSelectionSet(ref int, appendRef int)
		// ReplaceFragmentSpread marks to replace a fragment spread with a selectionset
		ReplaceFragmentSpread(selectionSet int, spreadRef int, replaceWithSelectionSet int)
		// ReplaceFragmentSpreadWithInlineFragment marks a fragment spread to be replaces with an inline fragment
		ReplaceFragmentSpreadWithInlineFragment(selectionSet int, spreadRef int, replaceWithSelectionSet int, typeCondition ast.TypeCondition)
		// ExpandInterfaceInlineFragment is used to replace an inline fragment
		// that has an interface type condition with corresponding inline
		// fragments that have concrete type conditions. The caller is expected
		// to pass in the appropriate concrete type names.
		ExpandInterfaceInlineFragment(inlineFragment int, parentSelectionSet int, concreteTypeNames []string)
		// PromoteUnionInlineFragments replaces an inline fragment that has a
		// union a type condition with its child selections. The child
		// selections within a union type are required to be fragments, so this
		// transformation can be described and "promoting" the child selections
		// to level of the original fragment.
		PromoteUnionInlineFragments(inlineFragment int, parentSelectionSet int)
		// ExpandInterfaceSelectionSet is used to replace the fields in a
		// selection set that has an interface enclosing type with inline
		// fragments that selection the same fields on the corresponding
		// concrete types that implement the interface.
		ExpandInterfaceSelectionSet(selectionSet int, concreteTypeNames []string)
	}
	transformation interface {
		apply(transformable Transformable)
	}
	// Precedence defines Depth and Order of each transformation
	Precedence struct {
		Depth int
		Order int
	}
	action struct {
		precedence     Precedence
		transformation transformation
	}
	// Transformer takes transformation registrations and applies them
	Transformer struct {
		actions []action
	}
)

// Reset empties all actions
func (t *Transformer) Reset() {
	t.actions = t.actions[:0]
}

// ApplyTransformations applies all registered transformations to a transformable
func (t *Transformer) ApplyTransformations(transformable Transformable) {
	sort.Slice(t.actions, func(i, j int) bool {
		if t.actions[i].precedence.Depth != t.actions[j].precedence.Depth {
			return t.actions[i].precedence.Depth > t.actions[j].precedence.Depth
		}
		return t.actions[i].precedence.Order < t.actions[j].precedence.Order
	})

	for i := range t.actions {
		t.actions[i].transformation.apply(transformable)
	}
}

// DeleteRootNode registers an action to delete a root node
func (t *Transformer) DeleteRootNode(precedence Precedence, node ast.Node) {
	t.actions = append(t.actions, action{
		precedence:     precedence,
		transformation: deleteRootNode{node: node},
	})
}

// EmptySelectionSet registers an actions to empty a selectionset
func (t *Transformer) EmptySelectionSet(precedence Precedence, ref int) {
	t.actions = append(t.actions, action{
		precedence:     precedence,
		transformation: emptySelectionSet{ref: ref},
	})
}

// AppendSelectionSet registers an action to append a selection to a selectionset
func (t *Transformer) AppendSelectionSet(precedence Precedence, ref int, appendRef int) {
	t.actions = append(t.actions, action{
		precedence: precedence,
		transformation: appendSelectionSet{
			ref:       ref,
			appendRef: appendRef,
		},
	})
}

// ReplaceFragmentSpread registers an action to replace a fragment spread with a selectionset
func (t *Transformer) ReplaceFragmentSpread(precedence Precedence, selectionSet int, spreadRef int, replaceWithSelectionSet int) {
	t.actions = append(t.actions, action{
		precedence: precedence,
		transformation: replaceFragmentSpread{
			selectionSet:            selectionSet,
			spreadRef:               spreadRef,
			replaceWithSelectionSet: replaceWithSelectionSet,
		},
	})
}

// ReplaceFragmentSpreadWithInlineFragment registers an action to replace a fragment spread with an inline fragment
func (t *Transformer) ReplaceFragmentSpreadWithInlineFragment(precedence Precedence, selectionSet int, spreadRef int, replaceWithSelectionSet int, typeCondition ast.TypeCondition) {
	t.actions = append(t.actions, action{
		precedence: precedence,
		transformation: replaceFragmentSpreadWithInlineFragment{
			selectionSet:            selectionSet,
			spreadRef:               spreadRef,
			replaceWithSelectionSet: replaceWithSelectionSet,
			typeCondition:           typeCondition,
		},
	})
}

func (t *Transformer) ExpandInterfaceInlineFragment(precedence Precedence, inlineFragment int, parentSelectionSet int, concreteTypeNames []string) {
	t.actions = append(t.actions, action{
		precedence: precedence,
		transformation: expandInterfaceInlineFragment{
			inlineFragment:     inlineFragment,
			parentSelectionSet: parentSelectionSet,
			concreteTypeNames:  concreteTypeNames,
		},
	})
}

func (t *Transformer) PromoteUnionInlineFragments(precedence Precedence, inlineFragment int, parentSelectionSet int) {
	t.actions = append(t.actions, action{
		precedence: precedence,
		transformation: promoteUnionInlineFragments{
			inlineFragment:     inlineFragment,
			parentSelectionSet: parentSelectionSet,
		},
	})
}

type replaceFragmentSpread struct {
	selectionSet            int
	spreadRef               int
	replaceWithSelectionSet int
}

func (r replaceFragmentSpread) apply(transformable Transformable) {
	transformable.ReplaceFragmentSpread(r.selectionSet, r.spreadRef, r.replaceWithSelectionSet)
}

type expandInterfaceInlineFragment struct {
	inlineFragment     int
	parentSelectionSet int
	concreteTypeNames  []string
}

func (e expandInterfaceInlineFragment) apply(transformable Transformable) {
	transformable.ExpandInterfaceInlineFragment(e.inlineFragment, e.parentSelectionSet, e.concreteTypeNames)
}

type promoteUnionInlineFragments struct {
	inlineFragment     int
	parentSelectionSet int
}

func (p promoteUnionInlineFragments) apply(transformable Transformable) {
	transformable.PromoteUnionInlineFragments(p.inlineFragment, p.parentSelectionSet)
}

type replaceFragmentSpreadWithInlineFragment struct {
	selectionSet            int
	spreadRef               int
	replaceWithSelectionSet int
	typeCondition           ast.TypeCondition
}

func (r replaceFragmentSpreadWithInlineFragment) apply(transformable Transformable) {
	transformable.ReplaceFragmentSpreadWithInlineFragment(r.selectionSet, r.spreadRef, r.replaceWithSelectionSet, r.typeCondition)
}

func (t *Transformer) ExpandInterfaceSelectionSet(precedence Precedence, selectionSet int, concreteTypeNames []string) {
	t.actions = append(t.actions, action{
		precedence: precedence,
		transformation: expandInterfaceSelectionSet{
			selectionSet:      selectionSet,
			concreteTypeNames: concreteTypeNames,
		},
	})
}

type expandInterfaceSelectionSet struct {
	selectionSet      int
	concreteTypeNames []string
}

func (e expandInterfaceSelectionSet) apply(transformable Transformable) {
	transformable.ExpandInterfaceSelectionSet(e.selectionSet, e.concreteTypeNames)
}

type deleteRootNode struct {
	node ast.Node
}

func (d deleteRootNode) apply(transformable Transformable) {
	transformable.DeleteRootNode(d.node)
}

type emptySelectionSet struct {
	ref int
}

func (e emptySelectionSet) apply(transformable Transformable) {
	transformable.EmptySelectionSet(e.ref)
}

type appendSelectionSet struct {
	ref       int
	appendRef int
}

func (a appendSelectionSet) apply(transformable Transformable) {
	transformable.AppendSelectionSet(a.ref, a.appendRef)
}
