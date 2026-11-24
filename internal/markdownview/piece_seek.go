package markdownview

// Seeking pays for an order check over the whole line. Small piece lists
// and a few fragments are cheaper to render with the ordinary scan.
const (
	minSeekPieces    = 16
	minSeekFragments = 4
)

func canSeekPieces(pieces []Piece) bool {
	if len(pieces) < minSeekPieces {
		return false
	}
	end := pieces[0].Start
	for _, p := range pieces {
		if p.Start < end || p.End < p.Start {
			return false
		}
		end = p.End
	}
	return true
}

func cellsInOrder(cells [][2]int) bool {
	for i := 1; i < len(cells); i++ {
		if cells[i-1][1] > cells[i][0] {
			return false
		}
	}
	return true
}

func skipPieces(pieces []Piece, cursor, start int) int {
	for cursor < len(pieces) && pieces[cursor].End <= start {
		cursor++
	}
	return cursor
}

// piecesAt returns candidates for [start, end) in their original order.
// Pieces are ordered and disjoint and starts never move back for this cursor.
// The next fragment can still intersect a piece that crosses its boundary.
func piecesAt(pieces []Piece, cursor *int, start, end int) []Piece {
	first := skipPieces(pieces, *cursor, start)
	*cursor = first
	last := first
	for last < len(pieces) && pieces[last].Start < end {
		last++
	}
	return pieces[first:last]
}
