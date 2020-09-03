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
	applicableParentTypes := make(map[string]*schema.Object, 0)
	parentType, ok := r.Schema.Types[e.Name]
	if !ok {
		panic(fmt.Errorf("cannot find type %q", e.Name))
	}
	switch pt := parentType.(type) {
	case *schema.Union:
		for _, t := range pt.PossibleTypes {
			applicableParentTypes[t.Name] = t
		}
	case *schema.Object:
		applicableParentTypes[pt.Name] = pt
	case *schema.Interface:
		for _, t := range pt.PossibleTypes {
			applicableParentTypes[t.Name] = t
		}
	}

	applicableFragmentTypes := make(map[string]*schema.Object, 0)
	fragmentType := r.Schema.Resolve(frag.On.Name)
	switch pt := fragmentType.(type) {
	case *schema.Union:
		for _, t := range pt.PossibleTypes {
			applicableFragmentTypes[t.Name] = t
		}
	case *schema.Object:
		applicableFragmentTypes[pt.Name] = pt
	case *schema.Interface:
		for _, t := range pt.PossibleTypes {
			applicableFragmentTypes[t.Name] = t
		}
	}

	applicableTypes := make(map[string]*schema.Object, 0)
	for k, t := range applicableFragmentTypes {
		if _, ok := applicableParentTypes[k]; ok {
			applicableTypes[k] = t
		}
	}

	if len(applicableTypes) == 0 {
		panic(fmt.Errorf("applicable types were empty"))
	}

	// If is not an inline spread
	if frag.On.Name != "" && frag.On.Name != e.Name {
		// If is interface, need to find the implementing object.
		if iface, ok := fragmentType.(*schema.Interface); ok {
			selections := []Selection{}
			for _, t := range iface.PossibleTypes {
				for _, i := range t.Interfaces {
					if i.Name == frag.On.Name {
						a, ok := applicableTypes[t.Name]
						if !ok {
							// If not a match on a type that is allowed in the union, skip.
							continue
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
				}
			}
			return selections
		}
		a, ok := applicableTypes[frag.On.Name]
		if !ok {
			panic(fmt.Errorf("invalid type spread on %q for fragment %q, applicableTypes: %+v. Available type assertions: %+v", e.Name, frag.On.Name, applicableTypes, e.TypeAssertions))
		}
		ta, ok := e.TypeAssertions[a.Name]
		if !ok {
			panic(fmt.Errorf("unknown type assertion for fragment %q", frag.On.Name))
		}
		return []Selection{&TypeAssertion{
			TypeAssertion: *ta,
			Sels:          applySelectionSet(r, s, ta.TypeExec.(*resolvable.Object), frag.Selections),
		}}
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
