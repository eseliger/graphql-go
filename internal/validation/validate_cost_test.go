package validation

import (
	"testing"

	"github.com/graph-gophers/graphql-go/internal/query"
	"github.com/graph-gophers/graphql-go/internal/schema"
)

const (
	simpleCostSchema = `
directive @cost(
	complexity: Int!
) on SCHEMA |
SCALAR |
OBJECT |
FIELD_DEFINITION |
ARGUMENT_DEFINITION |
INTERFACE |
UNION |
ENUM |
ENUM_VALUE |
INPUT_OBJECT |
INPUT_FIELD_DEFINITION

	schema {
		query: Query
	}

	type Query {
		characters: [Character]! @cost(complexity: 1)
	}

	type Character {
		id: ID!
		name: String!
		friends: [Character]!
	}`
	interfaceCostSimple = `schema {
		query: Query
	}

	type Query {
		characters: [Character]
	}

	interface Character {
		id: ID! @cost(complexity: 1)
		name: String! @cost(complexity: 1)
		friends: [Character] @cost(complexity: 1)
		appearsIn: [Episode]!
	}

	enum Episode {
		NEWHOPE
		EMPIRE
		JEDI
	}

	type Starship {}

	type Human implements Character {
		id: ID!
		name: String!
		friends: [Character]
		appearsIn: [Episode]!
		starships: [Starship]
		totalCredits: Int
	}

	type Droid implements Character {
		id: ID!
		name: String!
		friends: [Character]
		appearsIn: [Episode]!
		primaryFunction: String
	}`
)

type costTestCase struct {
	name     string
	query    string
	wantCost int
}

func (tc costTestCase) Run(t *testing.T, s *schema.Schema) {
	t.Run(tc.name, func(t *testing.T) {
		doc, qErr := query.Parse(tc.query)
		if qErr != nil {
			t.Fatal(qErr)
		}

		c := newContext(s, doc, 100000)
		op := doc.Operations[0]
		opc := &opContext{c, []*query.Operation{op}}

		cost := estimateCost(opc, op.Selections, map[string]int{}, getEntryPoint(c.schema, op))
		if have, want := cost, tc.wantCost; have != want {
			t.Fatalf("Got incorrect cost estimate, have=%d want=%d", have, want)
		}
	})
}

func TestCost(t *testing.T) {
	s := schema.New()

	err := s.Parse(simpleCostSchema, false)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []costTestCase{
		{
			name: "off",
			query: `query Okay {        # depth 0
			characters {         # depth 1
			  id                 # depth 2
			  name               # depth 2
			  friends {          # depth 2
					friends {    # depth 3
					  friends {  # depth 4
						  id       # depth 5
						  name     # depth 5
					  }
				  }
			  }
			}
		}`,
			wantCost: 8,
		}, {
			name: "maxDepth-1",
			query: `query Fine {        # depth 0
				characters {         # depth 1
				  id                 # depth 2
				  name               # depth 2
				  friends {          # depth 2
					  id               # depth 3
					  name             # depth 3
				  }
				}
			}`,
			wantCost: 6,
		}, {
			name: "maxDepth",
			query: `query Deep {        # depth 0
				characters {         # depth 1
				  id                 # depth 2
				  name               # depth 2
				  friends {          # depth 2
					  id               # depth 3
					  name             # depth 3
				  }
				}
			}`,
			wantCost: 6,
		},
		//  {
		// 	name: "maxDepth+1",
		// 	query: `query TooDeep {        # depth 0
		// 		characters {         # depth 1
		// 		  id                 # depth 2
		// 		  name               # depth 2
		// 		  friends {          # depth 2
		// 				friends {    # depth 3
		// 				  friends {  # depth 4
		// 					id       # depth 5
		// 					name     # depth 5
		// 				  }
		// 				}
		// 			}
		// 		}
		// 	}`,
		// 	wantCost: 8,
		// },
	} {
		tc.Run(t, s)
	}
}

// func TestCostInlineFragments(t *testing.T) {
// 	s := schema.New()

// 	err := s.Parse(interfaceCostSimple, false)
// 	if err != nil {
// 		t.Fatal(err)
// 	}

// 	for _, tc := range []costTestCase{
// 		{
// 			name: "maxDepth-1",
// 			query: `query { # depth 0
// 				characters { # depth 1
// 				  name # depth 2
// 				  ... on Human { # depth 2
// 					totalCredits # depth 2
// 				  }
// 				}
// 			  }`,
// 			depth: 3,
// 		},
// 		{
// 			name: "maxDepth",
// 			query: `query { # depth 0
// 				characters { # depth 1
// 				  ... on Droid { # depth 2
// 					primaryFunction # depth 2
// 				  }
// 				}
// 			  }`,
// 			depth: 2,
// 		},
// 		{
// 			name: "maxDepth+1",
// 			query: `query { # depth 0
// 				characters { # depth 1
// 				  ... on Droid { # depth 2
// 					primaryFunction # depth 2
// 				  }
// 				}
// 			  }`,
// 			depth:   1,
// 			failure: true,
// 		},
// 	} {
// 		tc.Run(t, s)
// 	}
// }

// func TestCostFragmentSpreads(t *testing.T) {
// 	s := schema.New()

// 	err := s.Parse(interfaceCostSimple, false)
// 	if err != nil {
// 		t.Fatal(err)
// 	}

// 	for _, tc := range []costTestCase{
// 		{
// 			name: "maxDepth-1",
// 			query: `fragment friend on Character {
// 				id  # depth 5
// 				name
// 				friends {
// 					name  # depth 6
// 				}
// 			}

// 			query {        # depth 0
// 				characters {         # depth 1
// 				  id                 # depth 2
// 				  name               # depth 2
// 				  friends {          # depth 2
// 					friends {        # depth 3
// 						friends {    # depth 4
// 							...friend # depth 5
// 						}
// 					}
// 				  }
// 				}
// 			}`,
// 			depth: 7,
// 		},
// 		{
// 			name: "maxDepth",
// 			query: `fragment friend on Character {
// 				id # depth 5
// 				name
// 			}
// 			query {        # depth 0
// 				characters {         # depth 1
// 				  id                 # depth 2
// 				  name               # depth 2
// 				  friends {          # depth 2
// 					friends {        # depth 3
// 						friends {    # depth 4
// 							...friend # depth 5
// 						}
// 					}
// 				  }
// 				}
// 			}`,
// 			depth: 5,
// 		},
// 		{
// 			name: "maxDepth+1",
// 			query: `fragment friend on Character {
// 				id # depth 6
// 				name
// 				friends {
// 					name # depth 7
// 				}
// 			}
// 			query {        # depth 0
// 				characters {         # depth 1
// 				  id                 # depth 2
// 				  name               # depth 2
// 				  friends {          # depth 2
// 					friends {        # depth 3
// 						friends {    # depth 4
// 						  friends {  # depth 5
// 							...friend # depth 6
// 						  }
// 						}
// 					}
// 				  }
// 				}
// 			}`,
// 			depth:   6,
// 			failure: true,
// 		},
// 	} {
// 		tc.Run(t, s)
// 	}
// }

// func TestCostUnknownFragmentSpreads(t *testing.T) {
// 	s := schema.New()

// 	err := s.Parse(interfaceCostSimple, false)
// 	if err != nil {
// 		t.Fatal(err)
// 	}

// 	for _, tc := range []costTestCase{
// 		{
// 			name: "maxDepthUnknownFragment",
// 			query: `query {        # depth 0
// 				characters {         # depth 1
// 				  id                 # depth 2
// 				  name               # depth 2
// 				  friends {          # depth 2
// 					friends {        # depth 3
// 						friends {    # depth 4
// 						  friends {  # depth 5
// 							...unknownFragment # depth 6
// 						  }
// 						}
// 					}
// 				  }
// 				}
// 			}`,
// 			depth:          6,
// 			failure:        true,
// 			expectedErrors: []string{"MaxDepthEvaluationError"},
// 		},
// 	} {
// 		tc.Run(t, s)
// 	}
// }

// func TestCostValidation(t *testing.T) {
// 	s := schema.New()

// 	err := s.Parse(interfaceCostSimple, false)
// 	if err != nil {
// 		t.Fatal(err)
// 	}

// 	for _, tc := range []struct {
// 		name     string
// 		query    string
// 		maxDepth int
// 		expected bool
// 	}{
// 		{
// 			name: "off",
// 			query: `query Fine {        # depth 0
// 				characters {         # depth 1
// 				  id                 # depth 2
// 				  name               # depth 2
// 				  friends {          # depth 2
// 					  id               # depth 3
// 					  name             # depth 3
// 				  }
// 				}
// 			}`,
// 			maxDepth: 0,
// 		}, {
// 			name: "fields",
// 			query: `query Fine {        # depth 0
// 				characters {         # depth 1
// 				  id                 # depth 2
// 				  name               # depth 2
// 				  friends {          # depth 2
// 					  id               # depth 3
// 					  name             # depth 3
// 				  }
// 				}
// 			}`,
// 			maxDepth: 2,
// 			expected: true,
// 		}, {
// 			name: "fragment",
// 			query: `fragment friend on Character {
// 				id # depth 6
// 				name
// 				friends {
// 					name # depth 7
// 				}
// 			}
// 			query {        # depth 0
// 				characters {         # depth 1
// 				  id                 # depth 2
// 				  name               # depth 2
// 				  friends {          # depth 2
// 					friends {        # depth 3
// 						friends {    # depth 4
// 						  friends {  # depth 5
// 							...friend # depth 6
// 						  }
// 						}
// 					}
// 				  }
// 				}
// 			}`,
// 			maxDepth: 5,
// 			expected: true,
// 		}, {
// 			name: "inlinefragment",
// 			query: `query { # depth 0
// 				characters { # depth 1
// 				  ... on Droid { # depth 2
// 					primaryFunction # depth 2
// 				  }
// 				}
// 			  }`,
// 			maxDepth: 1,
// 			expected: true,
// 		},
// 	} {
// 		t.Run(tc.name, func(t *testing.T) {
// 			doc, err := query.Parse(tc.query)
// 			if err != nil {
// 				t.Fatal(err)
// 			}

// 			context := newContext(s, doc, tc.maxDepth)
// 			op := doc.Operations[0]

// 			opc := &opContext{context: context, ops: doc.Operations}

// 			actual := validateMaxDepth(opc, op.Selections, 1)
// 			if actual != tc.expected {
// 				t.Errorf("expected %t, actual %t", tc.expected, actual)
// 			}
// 		})
// 	}
// }
