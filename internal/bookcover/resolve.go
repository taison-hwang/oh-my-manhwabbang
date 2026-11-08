package bookcover

import (
	"context"

	"shelf/internal/index"
	"shelf/internal/thumbs"
	"shelf/internal/userdata"
)

type UserData interface {
	GetCover(context.Context, string) (userdata.Cover, bool, error)
	ReadingDirLadder(context.Context, string, string) (*string, *string, error)
	GetSeriesPrefs(context.Context, string) (userdata.SeriesPrefs, error)
}

// Resolve shares the effective cover inputs between HTTP reads and scan pre-generation.
// The stored choice is returned even when it cannot currently be applied (E-69 §2.5).
//
// shared is true on a per-user server (ruling E-74 §2.13). A cover is one image
// every user is shown, so it must not depend on who asks: the direction is then
// the series' choice, else global, and global must be the configuration file's
// reader.reading_direction rather than anyone's settings. The caller supplies
// that; this function only declines to read the book rung.
func Resolve(ctx context.Context, user UserData, book index.BookRow, global string, shared bool) (thumbs.Request, *userdata.Cover, error) {
	c := thumbs.Cover{Dir: global}
	page := 1
	var stored *userdata.Cover
	if user != nil {
		choice, ok, err := user.GetCover(ctx, book.ID)
		if err != nil {
			return thumbs.Request{}, nil, err
		}
		if ok {
			stored = &choice
			if book.Status == "ok" && choice.Page >= 1 && int64(choice.Page) <= book.PageCount {
				page = choice.Page
				c.Region = &thumbs.Region{X: choice.X, Y: choice.Y, W: choice.W, H: choice.H}
			}
		}
		if c.Region == nil {
			var b, s *string
			if shared {
				sp, err := user.GetSeriesPrefs(ctx, book.SeriesID)
				if err != nil {
					return thumbs.Request{}, nil, err
				}
				s = sp.ReadingDir
			} else {
				var err error
				if b, s, err = user.ReadingDirLadder(ctx, book.ID, book.SeriesID); err != nil {
					return thumbs.Request{}, nil, err
				}
			}
			c.Dir = userdata.EffectiveReadingDir(b, s, global)
		}
	}
	return thumbs.Request{ID: book.ID, PageNo: page, ContentVersion: book.ContentVersion, Cover: &c, Priority: thumbs.PriorityCover}, stored, nil
}
