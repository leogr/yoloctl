package markdownview

// The follow view already admits 2 MiB whole files (followview.WholeBytes).
// Reuse that size as a floor for viewport-constrained output: ordinary wrapped
// tables may include several bytes of rules/padding per source byte. The
// source-relative allowance still wins for larger inputs; neither permits
// unbounded sparse padding across hundreds of wrapped rows/columns.
const tableViewBytes = 2 << 20

func tableLayoutBudget(tbl *TableInfo, width int) int {
	sourceBudget := 2*tbl.Source + TableSlack
	natural := 3 * (len(tbl.Natural) - 1)
	for _, w := range tbl.Natural {
		natural += w
	}
	// Natural-fit output is the aligned layout. Never rebuild padding that
	// TableLines rejected just because the viewport is now very wide.
	if width >= natural {
		return sourceBudget
	}
	return max(sourceBudget, tableViewBytes)
}
