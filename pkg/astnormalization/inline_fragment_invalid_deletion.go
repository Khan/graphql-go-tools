package astnormalization

import (
	"github.com/jensneuse/graphql-go-tools/pkg/ast"
	"github.com/jensneuse/graphql-go-tools/pkg/astvisitor"
)

func deleteInvalidInlineFragments(walker *astvisitor.Walker) {
	visitor := deleteInvalidInlineFragmentsVisitor{
		Walker: walker,
	}
	walker.RegisterEnterDocumentVisitor(&visitor)
	walker.RegisterEnterSelectionSetVisitor(&visitor)
}

type deleteInvalidInlineFragmentsVisitor struct {
	*astvisitor.Walker
	operation, definition *ast.Document
}

func (m *deleteInvalidInlineFragmentsVisitor) EnterDocument(operation, definition *ast.Document) {
	m.operation = operation
	m.definition = definition
}

func (d *deleteInvalidInlineFragmentsVisitor) EnterSelectionSet(ref int) {
	selections := d.operation.SelectionSets[ref].SelectionRefs

	if len(selections) == 0 {
		return
	}

	for index := len(selections) - 1; index >= 0; index -= 1 {
		if d.operation.Selections[selections[index]].Kind != ast.SelectionKindInlineFragment {
			continue
		}

		inlineFragment := d.operation.Selections[selections[index]].Ref

		typeName := d.operation.InlineFragmentTypeConditionName(inlineFragment)

		node, exists := d.definition.Index.FirstNonExtensionNodeByNameBytes(typeName)
		if !exists {
			continue
		}

		// Both the fragment type and enclosing type should be objects at this
		// point due to interface selection set expansion and inline fragment
		// abstract type expansion.
		//
		// TODO: consider comparing the type names directly instead of calling
		// NodeFragmentIsAllowedOnNode.
		if !d.definition.NodeFragmentIsAllowedOnNode(node, d.EnclosingTypeDefinition) {
			d.operation.RemoveFromSelectionSet(ref, index)
		}
	}
}
