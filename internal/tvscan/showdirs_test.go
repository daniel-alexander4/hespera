package tvscan

import (
	"reflect"
	"testing"
)

func TestShowDirsForFiles(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			"season-organized → the show folder (grandparent), deduped",
			[]string{
				"/media/tv/Survivor/Season 1/Survivor.S01E01.mkv",
				"/media/tv/Survivor/Season 1/Survivor.S01E02.mkv",
				"/media/tv/Survivor/Season 49/Survivor.S49E01.mkv",
			},
			[]string{"/media/tv/Survivor"},
		},
		{
			"flat layout → the file's own folder",
			[]string{
				"/media/tv/Burnistoun/ep1.mkv",
				"/media/tv/Burnistoun/ep2.mkv",
			},
			[]string{"/media/tv/Burnistoun"},
		},
		{
			"scattered across two show folders → both (a couple folders is fine)",
			[]string{
				"/media/tv/Survivor/Season 1/a.mkv",
				"/media/extra/Survivor Specials/b.mkv",
			},
			[]string{"/media/tv/Survivor", "/media/extra/Survivor Specials"},
		},
		{
			"specials folder counts as a season dir (→ show folder)",
			[]string{"/media/tv/Survivor/Specials/special.mkv"},
			[]string{"/media/tv/Survivor"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShowDirsForFiles(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ShowDirsForFiles = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDropNestedRoots(t *testing.T) {
	got := dropNestedRoots([]string{"/media/tv/Survivor", "/media/tv/Survivor/Season 1", "/media/tv/Other"})
	want := []string{"/media/tv/Survivor", "/media/tv/Other"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dropNestedRoots = %v, want %v", got, want)
	}
}
