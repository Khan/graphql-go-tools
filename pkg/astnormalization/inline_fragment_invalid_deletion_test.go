package astnormalization

import "testing"

func TestDeleteInvalidInlineFragments(t *testing.T) {
	t.Run("incompatible type", func(t *testing.T) {
		run(deleteInvalidInlineFragments, testDefinition, `
					query testQuery {
						dog {
							... on Dog {
								barkVolume
							}
							... on Cat {
								meowVolume
							}
						}
					}`,
			`
					query testQuery {
						dog {
							... on Dog {
								barkVolume
							}
						}
					}`)
	})
	t.Run("empty selection", func(t *testing.T) {
		run(deleteInvalidInlineFragments, testDefinition, `
					query testQuery {
						dog {
							... on Dog {
								barkVolume
							}
							... on Dog {}
						}
					}`,
			`
					query testQuery {
						dog {
							... on Dog {
								barkVolume
							}
						}
					}`)
	})
}
