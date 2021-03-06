package selected

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/graph-gophers/graphql-go/errors"
	"github.com/graph-gophers/graphql-go/internal/common"
	"github.com/graph-gophers/graphql-go/internal/exec/packer"
	"github.com/graph-gophers/graphql-go/internal/exec/resolvable"
	"github.com/graph-gophers/graphql-go/internal/query"
	"github.com/graph-gophers/graphql-go/internal/schema"
	"github.com/graph-gophers/graphql-go/introspection"
)

type Request struct {
	Schema               *schema.Schema
	Doc                  *query.Document
	Vars                 map[string]interface{}
	Mu                   sync.Mutex
	Errs                 []*errors.QueryError
	DisableIntrospection bool
}

func (r *Request) AddError(err *errors.QueryError) {
	r.Mu.Lock()
	r.Errs = append(r.Errs, err)
	r.Mu.Unlock()
}

func ApplyOperation(r *Request, s *resolvable.Schema, op *query.Operation) []Selection {
	var obj *resolvable.Object
	switch op.Type {
	case query.Query:
		obj = s.Query.(*resolvable.Object)
	case query.Mutation:
		obj = s.Mutation.(*resolvable.Object)
	case query.Subscription:
		obj = s.Subscription.(*resolvable.Object)
	}
	return applySelectionSet(r, s, obj, op.Selections)
}

type Selection interface {
	isSelection()
}

type SchemaField struct {
	resolvable.Field
	Alias       string
	Args        map[string]interface{}
	PackedArgs  reflect.Value
	Sels        []Selection
	Async       bool
	FixedResult reflect.Value
}

type TypeAssertion struct {
	resolvable.TypeAssertion
	Sels []Selection
}

type TypenameField struct {
	resolvable.Object
	Alias string
}

func (*SchemaField) isSelection()   {}
func (*TypeAssertion) isSelection() {}
func (*TypenameField) isSelection() {}

func applySelectionSet(r *Request, s *resolvable.Schema, e *resolvable.Object, sels []query.Selection) (flattenedSels []Selection) {
	for _, sel := range sels {
		switch sel := sel.(type) {
		case *query.Field:
			field := sel
			if skipByDirective(r, field.Directives) {
				continue
			}

			switch field.Name.Name {
			case "__typename":
				if !r.DisableIntrospection {
					flattenedSels = append(flattenedSels, &TypenameField{
						Object: *e,
						Alias:  field.Alias.Name,
					})
				}

			case "__schema":
				if !r.DisableIntrospection {
					flattenedSels = append(flattenedSels, &SchemaField{
						Field:       s.Meta.FieldSchema,
						Alias:       field.Alias.Name,
						Sels:        applySelectionSet(r, s, s.Meta.Schema, field.Selections),
						Async:       true,
						FixedResult: reflect.ValueOf(introspection.WrapSchema(r.Schema)),
					})
				}

			case "__type":
				if !r.DisableIntrospection {
					p := packer.ValuePacker{ValueType: reflect.TypeOf("")}
					v, err := p.Pack(field.Arguments.MustGet("name").Value(r.Vars))
					if err != nil {
						r.AddError(errors.Errorf("%s", err))
						return nil
					}

					t, ok := r.Schema.Types[v.String()]
					if !ok {
						return nil
					}

					flattenedSels = append(flattenedSels, &SchemaField{
						Field:       s.Meta.FieldType,
						Alias:       field.Alias.Name,
						Sels:        applySelectionSet(r, s, s.Meta.Type, field.Selections),
						Async:       true,
						FixedResult: reflect.ValueOf(introspection.WrapType(t)),
					})
				}

			default:
				fe := e.Fields[field.Name.Name]

				var args map[string]interface{}
				var packedArgs reflect.Value
				if fe.ArgsPacker != nil {
					args = make(map[string]interface{})
					for _, arg := range field.Arguments {
						args[arg.Name.Name] = arg.Value.Value(r.Vars)
					}
					var err error
					packedArgs, err = fe.ArgsPacker.Pack(args)
					if err != nil {
						r.AddError(errors.Errorf("%s", err))
						return
					}
				}

				fieldSels := applyField(r, s, fe.ValueExec, field.Selections)
				flattenedSels = append(flattenedSels, &SchemaField{
					Field:      *fe,
					Alias:      field.Alias.Name,
					Args:       args,
					PackedArgs: packedArgs,
					Sels:       fieldSels,
					Async:      fe.HasContext || fe.ArgsPacker != nil || fe.HasError || HasAsyncSel(fieldSels),
				})
			}

		case *query.InlineFragment:
			frag := sel
			if skipByDirective(r, frag.Directives) {
				continue
			}
			flattenedSels = append(flattenedSels, applyFragment(r, s, e, &frag.Fragment)...)

		case *query.FragmentSpread:
			spread := sel
			if skipByDirective(r, spread.Directives) {
				continue
			}
			flattenedSels = append(flattenedSels, applyFragment(r, s, e, &r.Doc.Fragments.Get(spread.Name.Name).Fragment)...)

		default:
			panic("invalid type")
		}
	}
	return
}

func applyFragment(r *Request, s *resolvable.Schema, e *resolvable.Object, frag *query.Fragment) []Selection {
	// If is not an inline spread, and not a spread on the same type as the parent type.
	if frag.On.Name != "" && frag.On.Name != e.Name {
		parentType := r.Schema.Resolve(e.Name)
		fragmentType := r.Schema.Resolve(frag.On.Name)
		// If the parent is an interface, only implementing types are allowed,
		// so we can return the type assertion for the object straight away.
		if _, ok := parentType.(*schema.Interface); ok {
			ta, ok := e.TypeAssertions[frag.On.Name]
			if !ok {
				panic(fmt.Errorf("unknown type assertion for fragment %q", frag.On.Name))
			}
			return []Selection{&TypeAssertion{
				TypeAssertion: *ta,
				Sels:          applySelectionSet(r, s, ta.TypeExec.(*resolvable.Object), frag.Selections),
			}}
		}
		// Otherwise, the parent can be a union or an object.
		// If it's an object, just apply the selection set.
		if _, ok := parentType.(*schema.Object); ok {
			if _, ok := fragmentType.(*schema.Interface); ok {
				// Assume the object implements the interface. This should already be checked before in the validating steps.
				return applySelectionSet(r, s, e, frag.Selections)
			}
			if _, ok := fragmentType.(*schema.Object); ok {
				// If is object .. object selection, it cannot match, because names have been compared above already.
				return nil
			}
			// Object ... Union can simply be expanded.
			return applySelectionSet(r, s, e, frag.Selections)
		}

		// Otherwise, the parent type can only be a union.

		// If the fragment type is an object, we can apply the selection straight away,
		// validation should already have checked that the object is an element of the
		// allowed types of the union.
		if _, ok := fragmentType.(*schema.Object); ok {
			ta, ok := e.TypeAssertions[frag.On.Name]
			if !ok {
				panic(fmt.Errorf("unknown type assertion for fragment %q", frag.On.Name))
			}
			// Need to do a type assertion first, on a union, only one of the types matches,
			// so N - 1 other types won't match and should not be selected.
			return []Selection{&TypeAssertion{
				TypeAssertion: *ta,
				Sels:          applySelectionSet(r, s, ta.TypeExec.(*resolvable.Object), frag.Selections),
			}}
		}

		// The fragment type needs to be an interface on a union at this point,
		// we need to first check if the interface applies:
		// It applies, when at least one of the possible types of the union implements
		// the interface we're spreading here.
		applicableParentTypes := make(map[string]*schema.Object, 0)
		for _, t := range schema.PossibleTypes(parentType) {
			applicableParentTypes[t.Name] = t
		}

		applicableFragmentTypes := make(map[string]*schema.Object, 0)
		for _, t := range schema.PossibleTypes(fragmentType) {
			applicableFragmentTypes[t.Name] = t
		}

		applicableTypes := make(map[string]*schema.Object, 0)
		for k, t := range applicableFragmentTypes {
			if _, ok := applicableParentTypes[k]; ok {
				applicableTypes[k] = t
			}
		}

		// GraphQL spec says: If the intersection of the applicable types of fragment and parent
		// is an empty set, it doesn't apply. (This is already validated before).
		if len(applicableTypes) == 0 {
			panic(fmt.Errorf("applicable types were empty"))
		}

		// Now, we need to resolve the interface to the possible types.
		iface := fragmentType.(*schema.Interface) // interface Character
		// Find all types in the union, that implement the interface.
		implementingTypes := make([]*schema.Object, 0)
		for _, t := range iface.PossibleTypes { // Look over all types that implement the interface.
			if _, ok := applicableParentTypes[t.Name]; ok { // But only take the ones that satisfy the union.
				implementingTypes = append(implementingTypes, t)
			}
		}
		// Now we return a selection of type assertions to all the implementing types, so every instance will have those fields selected.
		selections := make([]Selection, 0)
		for _, typ := range implementingTypes {
			a, ok := applicableTypes[typ.Name]
			if !ok {
				panic(fmt.Errorf("unknown type %q", typ.Name))
			}
			ta, ok := e.TypeAssertions[a.Name]
			if !ok {
				panic(fmt.Errorf("unknown type assertion for fragment %q", frag.On.Name))
			}
			selections = append(selections, &TypeAssertion{
				TypeAssertion: *ta,
				Sels:          applySelectionSet(r, s, ta.TypeExec.(*resolvable.Object), frag.Selections),
			})
		}
		return selections
	}
	return applySelectionSet(r, s, e, frag.Selections)
}

func applyField(r *Request, s *resolvable.Schema, e resolvable.Resolvable, sels []query.Selection) []Selection {
	switch e := e.(type) {
	case *resolvable.Object:
		return applySelectionSet(r, s, e, sels)
	case *resolvable.List:
		return applyField(r, s, e.Elem, sels)
	case *resolvable.Scalar:
		return nil
	default:
		panic("unreachable")
	}
}

func skipByDirective(r *Request, directives common.DirectiveList) bool {
	if d := directives.Get("skip"); d != nil {
		p := packer.ValuePacker{ValueType: reflect.TypeOf(false)}
		v, err := p.Pack(d.Args.MustGet("if").Value(r.Vars))
		if err != nil {
			r.AddError(errors.Errorf("%s", err))
		}
		if err == nil && v.Bool() {
			return true
		}
	}

	if d := directives.Get("include"); d != nil {
		p := packer.ValuePacker{ValueType: reflect.TypeOf(false)}
		v, err := p.Pack(d.Args.MustGet("if").Value(r.Vars))
		if err != nil {
			r.AddError(errors.Errorf("%s", err))
		}
		if err == nil && !v.Bool() {
			return true
		}
	}

	return false
}

func HasAsyncSel(sels []Selection) bool {
	for _, sel := range sels {
		switch sel := sel.(type) {
		case *SchemaField:
			if sel.Async {
				return true
			}
		case *TypeAssertion:
			if HasAsyncSel(sel.Sels) {
				return true
			}
		case *TypenameField:
			// sync
		default:
			panic("unreachable")
		}
	}
	return false
}
