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
) on SCHEMA | SCALAR | OBJECT | FIELD_DEFINITION | ARGUMENT_DEFINITION | INTERFACE | UNION | ENUM | ENUM_VALUE | INPUT_OBJECT | INPUT_FIELD_DEFINITION

	schema {
		query: Query
	}

	type Query {
		characters: [FriendOrEnemy]! @cost(complexity: 1)
		friend(id: ID!): Friend!
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
	name      string
	query     string
	variables map[string]interface{}
	wantCost  int
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

		cost := estimateCost(opc, tc.variables, op.Selections, getEntryPoint(c.schema, op))
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
		{
			name: "takes complexity for whole union when also querying fields outside of fragment",
			query: `
			query {
				characters {
				  name
				  ... on Character {
					  id
				  }
				  ... on Enemy {
					  weapon
				  }
				}
			  }
		`,
			wantCost: (9) + (1) + 1,
		},
		{
			name: "takes complexity for whole union when also querying fields outside of fragment, below",
			query: `
			query {
				characters {
					... on Character {
						id
					}
					... on Enemy {
						weapon
					}
					name
				}
			  }
		`,
			wantCost: (9) + (1) + 1,
		},
		{
			name: "interface merging",
			query: `
			query {
				friend {
					... on Character { # total cost including outer fields: 1 + 2 = 3
						id # cost 1
						# name costs 2 on Characters, so taking that cost.
					}
					... on Enemy { # total cost including outer fields: 9 + 1 = 10
						weapon # cost 9
					}
					name # cost 1 on interface
				}
			  }
		`,
			wantCost: (9) + (1),
		},
		{
			name: "union without fragments",
			query: `
			query {
				characters {
					name # cost 1 on interface
				}
			  }
		`,
			wantCost: 1 + 1,
		},
		{
			name: "doesn't charge for skip true",
			query: `
			query {
				characters { # costs 1
					... on Character {
						id # costs 1
						name @skip(if: true) # costs 2, but should be skipped
					}
				}
			}
			`,
			wantCost: 1 + 1,
		},
		{
			name: "doesn't charge for skip true on inline fragment",
			query: `
				query {
					characters { # costs 1
						... on Character {
							id # costs 1
							... @skip(if: true) {
								name # costs 2, but should be skipped
							}
						}
					}
				}
			`,
			wantCost: 1 + 1,
		},
		{
			name: "does charge for skip false",
			query: `
			query {
				characters { # costs 1
					... on Character {
						id # costs 1
						name @skip(if: false) # costs 2
					}
				}
			  }
		`,
			wantCost: 1 + 2 + 1,
		},
		{
			name: "does charge for include true",
			query: `
			query {
				characters { # costs 1
					... on Character {
						id # costs 1
						name @include(if: true) # costs 2
					}
				}
			  }
		`,
			wantCost: 1 + 2 + 1,
		},
		{
			name: "doesn't charge for include false",
			query: `
			query {
				characters { # costs 1
					... on Character {
						id # costs 1
						name @include(if: false) # costs 2, but should not be included in cost
					}
				}
			  }
		`,
			wantCost: 1 + 1,
		},
		{
			name: "doesn't charge for include false on inline fragment",
			query: `
			query {
				characters { # costs 1
					... on Character {
						id # costs 1
						... @include(if: false) {
							name # costs 2, but should be skipped
						}
					}
				}
			  }
		`,
			wantCost: 1 + 1,
		},
		{
			name: "doesn't charge for skip true from variable",
			query: `
			query ($skip: Boolean!) {
				characters { # costs 1
					... on Character {
						id # costs 1
						name @skip(if: $skip) # costs 2, but should be skipped
					}
				}
			  }
		`,
			variables: map[string]interface{}{"skip": true},
			wantCost:  1 + 1,
		},
		{
			name: "does charge for skip false from variable",
			query: `
			query ($skip: Boolean!) {
				characters { # costs 1
					... on Character {
						id # costs 1
						name @skip(if: $skip) # costs 2
					}
				}
			  }
		`,
			variables: map[string]interface{}{"skip": false},
			wantCost:  1 + 2 + 1,
		},
		{
			name: "does charge for include true from variable",
			query: `
			query ($include: Boolean!) {
				characters { # costs 1
					... on Character {
						id # costs 1
						name @include(if: $include) # costs 2
					}
				}
			  }
		`,
			variables: map[string]interface{}{"include": true},
			wantCost:  1 + 2 + 1,
		},
		{
			name: "doesn't charge for include false from variable",
			query: `
			query ($include: Boolean!) {
				characters { # costs 1
					... on Character {
						id # costs 1
						name @include(if: $include) # costs 2, but should not be included in cost
					}
				}
			  }
		`,
			variables: map[string]interface{}{"include": false},
			wantCost:  1 + 1,
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
