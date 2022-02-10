package astnormalization

import "testing"

func TestDeleteInvalidInlineFragments(t *testing.T) {
	t.Run("simple", func(t *testing.T) {
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
}
