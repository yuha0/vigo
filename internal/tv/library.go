package tv

import (
	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
)

// LibraryHook is told about changes to the TV's content so the library can
// keep its mirror current. Calls are made from the TV event loop and must
// not block.
type LibraryHook interface {
	// TVContentAdded reports an image_added event or a completed upload.
	TVContentAdded(contentID string)
	// TVContentDeleted reports content proven deleted: an image_deleted
	// event or a delete_image_list reply.
	TVContentDeleted(contentID string)
	// TVRefreshed reports a TVService.Refresh.
	TVRefreshed()
}

// SetLibraryHook installs h; nil removes it.
func (s *Service) SetLibraryHook(h LibraryHook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hook = h
}

func (s *Service) libraryHook() LibraryHook {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hook
}

// SetWarning adds or updates a warning in Status.
func (s *Service) SetWarning(code, msg string) { s.setWarning(code, msg) }

// ClearWarning removes a warning from Status.
func (s *Service) ClearWarning(code string) { s.clearWarning(code) }

// EmitLibraryChanged tells WatchEvents subscribers that artworks or sources
// changed.
func (s *Service) EmitLibraryChanged(artworkIDs, sourceIDs []string) {
	s.emit(&corev1.WatchEventsResponse{Event: &corev1.WatchEventsResponse_LibraryChanged{
		LibraryChanged: &corev1.LibraryChanged{ArtworkIds: artworkIDs, SourceIds: sourceIDs},
	}})
}
