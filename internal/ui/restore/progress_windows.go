package restore

import "github.com/restic/restic/internal/restic"

// incrementFilesFinished increments the files finished count if it is a main file
func (p *Progress) incrementFilesFinished(attrs []restic.GenericAttribute) {
	if restic.GetGenericAttribute(restic.TypeIsADS, attrs) == nil {
		p.filesFinished++
	}
}
