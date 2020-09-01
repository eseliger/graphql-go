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
	multipliers: [String!]
	useMultipliers: Boolean = true
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
		characters: [FriendOrEnemy]! @cost(complexity: 1)
	}

	interface Friend {
		name: String! @cost(complexity: 1)
	}

	type Enemy implements Friend {
		name: String!
		weapon: String! @cost(complexity: 9)
	}

	union FriendOrEnemy = Character | Enemy

	type FriendConnection {
		totalCount: Int! @cost(complexity: 1, useMultipliers: false)
		nodes: [Character!]!
	}

	type Character implements Friend {
		id: ID! @cost(complexity: 1)
		name: String! @cost(complexity: 2)
		friends(first: Int, last: Int): [Friend]! @cost(multipliers: ["first", "last"])
		bestFriends(first: Int): FriendConnection! @cost(multipliers: ["first"], complexity: 3)
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

		cost := estimateCost(opc, op.Selections, getEntryPoint(c.schema, op))
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
			name: "complex",
			query: `
			query Okay {
				characters {
				  name
				  ... on Character {
				  id
				  friends(first: 4, last: 2) {
					... on Character {
					  friends {
						... on Character {
						  friends {
							...friendsFields
							... on Character {
								bestFriends(first: 2) {
									totalCount
									nodes {
										...friendsFields
									}
								}
							}
						  }
						}
					  }
					}
				  }
				  }
				  ...enemyFields
				}
			  }
			  
			  fragment friendsFields on Character {
				id
				name
			  }

			  fragment enemyFields on Enemy {
				  name
				  weapon
			  }
			  
		`,
			wantCost: 82,
		},
		{
			name: "useMultipliers false on one field",
			query: `
			query {
				characters {
				  ... on Character {
					bestFriends(first: 5) {
						totalCount
						nodes {
							...friendsFields
						}
					}
				  }
				}
			  }
			  
			  fragment friendsFields on Character {
				id
				name
			  }
		`,
			wantCost: (1+2)*5 + 1 + 3 + 1,
		},
		{
			name: "takes complexity from interface if type has no annotation",
			query: `
			query {
				characters {
				  ... on Enemy {
				    name
				  }
				}
			  }
		`,
			wantCost: 1 + 1,
		},
		{
			name: "sums up multipliers",
			query: `
			query {
				characters {
				  ... on Character {
				    friends(first: 5, last: 10) {
						name # This field costs less than if it was requested on the Character itself. I think this is fine though, user needs to take care of precedence.
					}
				  }
				}
			  }
		`,
			wantCost: 1*(5+10) + 1,
		},
		{
			name: "cost from fragment",
			query: `
			query {
				characters {
				  ...CharacterFields
				}
			  }

			  fragment CharacterFields on Character {
				  id
				  name
			  }
		`,
			wantCost: (1 + 2) + 1,
		},
		{
			name: "cost from fragment on interface",
			query: `
			query {
				characters {
				  ...FriendFields
				}
			  }

			  fragment FriendFields on Friend {
				  name
			  }
		`,
			wantCost: (1) + 1,
		},
		{
			name: "takes cost from more expensive union type",
			query: `
			query {
				characters {
				  ... on Friend {
					  name
				  }
				  ... on Enemy {
					  weapon
				  }
				}
			  }
		`,
			wantCost: (9) + 1,
		},
	} {
		tc.Run(t, s)
	}

	t.Run("correctly fails query if cost is too high", func(t *testing.T) {
		doc, err := query.Parse(`query { characters { ... on Character { friends(first: 100) { name } } } }`)
		if err != nil {
			t.Fatal(err)
		}
		// Cost of the query is 101, should fail if 100 is the limit.
		errs := Validate(s, doc, nil, 0, 100)
		if len(errs) != 1 {
			t.Fatalf("got incorrect amount of errors back: %d", len(errs))
		}
	})
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
