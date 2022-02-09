package astnormalization

import "testing"

func TestExpandInterfaceSelections(t *testing.T) {
	t.Run("simple", func(t *testing.T) {
		run(expandInterfaceSelectionSets, testDefinition, `
					query testQuery {
						pet {
							nameOne: name
							... on Dog {
								barkVolume
							}
							nameTwo: name
							... on Cat {
								meowVolume
							}
							nameThree: name
						}
					}`,
			`
					query testQuery {
						pet {
							... on Dog {
								barkVolume
							}
							... on Cat {
								meowVolume
							}
							... on Dog {
								nameOne: name
								nameTwo: name
								nameThree: name
							}
							... on Cat {
								nameOne: name
								nameTwo: name
								nameThree: name
							}
						}
					}`)
	})

	t.Run("already expanded", func(t *testing.T) {
		run(expandInterfaceSelectionSets, testDefinition, `
					query testQuery {
						pet {
							... on Dog {
								name
							}
							... on Cat {
								name
							}
						}
					}`,
			`
					query testQuery {
						pet {
							... on Dog {
								name
							}
							... on Cat {
								name
							}
						}
					}`)
	})

	t.Run("already expanded with typename", func(t *testing.T) {
		run(expandInterfaceSelectionSets, testDefinition, `
					query testQuery {
						pet {
							__typename
							... on Dog {
								name
							}
							... on Cat {
								name
							}
						}
					}`,
			`
					query testQuery {
						pet {
							__typename
							... on Dog {
								name
							}
							... on Cat {
								name
							}
						}
					}`)
	})
}
