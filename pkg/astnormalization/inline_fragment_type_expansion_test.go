package astnormalization

import "testing"

func TestExpandInlineFragmentTypes(t *testing.T) {
	t.Run("simple", func(t *testing.T) {
		run(expandAbstractInlineFragments, testDefinition, `
					query testQuery {
						dog {
							... on Pet {
								petName: name
								... on Pet {
									nestedPetName: name
								}
								... on Cat {
									catName: name
								}
							}
							... on CatOrDog {
								... on Dog {
									catOrDogName: name
								}
							}
							... on Dog {
								dogName: name
							}
						}
					}`,
			`
					query testQuery {
						dog {
							... on Dog {
								petName: name
								... on Dog {
									nestedPetName: name
								}
								... on Cat {
									nestedPetName: name
								}
								... on Cat {
									catName: name
								}
							}... on Cat {
								petName: name
								... on Dog {
									nestedPetName: name
								}
								... on Cat {
									nestedPetName: name
								}
								... on Cat {
									catName: name
								}
							}
							... on Dog {
								catOrDogName: name
							}
							... on Dog {
								dogName: name
							}
						}
					}`)
	})
}
