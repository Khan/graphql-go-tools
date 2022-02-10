package astnormalization

import (
	"github.com/jensneuse/graphql-go-tools/pkg/ast"
	"github.com/jensneuse/graphql-go-tools/pkg/asttransform"
	"github.com/jensneuse/graphql-go-tools/pkg/astvisitor"
)

func expandAbstractInlineFragments(walker *astvisitor.Walker) {
	visitor := expandAbstractInlineFragmentsVisitor{
		Walker: walker,
	}
	walker.RegisterDocumentVisitor(&visitor)
	walker.RegisterEnterSelectionSetVisitor(&visitor)
}

type expandAbstractInlineFragmentsVisitor struct {
	*astvisitor.Walker
	operation   *ast.Document
	definition  *ast.Document
	transformer asttransform.Transformer
}

func (e *expandAbstractInlineFragmentsVisitor) EnterDocument(operation, definition *ast.Document) {
	e.transformer.Reset()
	e.operation = operation
	e.definition = definition
}

func (e *expandAbstractInlineFragmentsVisitor) LeaveDocument(operation, definition *ast.Document) {
	e.transformer.ApplyTransformations(operation)
}

func (e *expandAbstractInlineFragmentsVisitor) EnterSelectionSet(ref int) {
	inlineFragmentNode := e.Walker.Ancestors[len(e.Walker.Ancestors)-1]

	if inlineFragmentNode.Kind != ast.NodeKindInlineFragment {
		return
	}

	if e.Walker.EnclosingTypeDefinition.Kind != ast.NodeKindInterfaceTypeDefinition &&
		e.Walker.EnclosingTypeDefinition.Kind != ast.NodeKindUnionTypeDefinition {
		return
	}

	parentSelectionSet := e.Walker.Ancestors[len(e.Walker.Ancestors)-2].Ref

	precedence := asttransform.Precedence{
		Depth: e.Walker.Depth,
		Order: 0,
	}

	switch e.Walker.EnclosingTypeDefinition.Kind {
	case ast.NodeKindInterfaceTypeDefinition:
		typeNames := e.definition.ConcreteInterfaceImplementationTypeNames(e.Walker.EnclosingTypeDefinition.Ref)
		e.transformer.ExpandInterfaceInlineFragment(precedence, inlineFragmentNode.Ref, parentSelectionSet, typeNames)
	case ast.NodeKindUnionTypeDefinition:
		e.transformer.PromoteUnionInlineFragments(precedence, inlineFragmentNode.Ref, parentSelectionSet)
	}
}
