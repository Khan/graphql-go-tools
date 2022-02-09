package astnormalization

import (
	"github.com/jensneuse/graphql-go-tools/pkg/ast"
	"github.com/jensneuse/graphql-go-tools/pkg/asttransform"
	"github.com/jensneuse/graphql-go-tools/pkg/astvisitor"
)

func expandInterfaceSelectionSets(walker *astvisitor.Walker) {
	visitor := expandInterfaceSelectionSetsVisitor{
		Walker: walker,
	}
	walker.RegisterDocumentVisitor(&visitor)
	walker.RegisterEnterSelectionSetVisitor(&visitor)
}

type expandInterfaceSelectionSetsVisitor struct {
	*astvisitor.Walker
	operation   *ast.Document
	definition  *ast.Document
	transformer asttransform.Transformer
}

func (e *expandInterfaceSelectionSetsVisitor) EnterDocument(operation, definition *ast.Document) {
	e.transformer.Reset()
	e.operation = operation
	e.definition = definition
}

func (e *expandInterfaceSelectionSetsVisitor) LeaveDocument(operation, definition *ast.Document) {
	e.transformer.ApplyTransformations(operation)
}

func (e *expandInterfaceSelectionSetsVisitor) EnterSelectionSet(ref int) {
	if e.Walker.EnclosingTypeDefinition.Kind != ast.NodeKindInterfaceTypeDefinition {
		return
	}
	parent := e.Walker.Ancestors[len(e.Walker.Ancestors)-1]
	if parent.Kind != ast.NodeKindField {
		return
	}
	precedence := asttransform.Precedence{
		Depth: e.Walker.Depth,
		Order: 0,
	}
	typeNames := e.definition.ConcreteInterfaceImplementationTypeNames(e.Walker.EnclosingTypeDefinition.Ref)
	e.transformer.ExpandInterfaceSelectionSet(precedence, ref, typeNames)
}
