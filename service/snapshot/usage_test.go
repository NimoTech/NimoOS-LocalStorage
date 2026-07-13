package snapshot

import "testing"

func TestBtrfsUsagePercent(t *testing.T) {
	cases := []struct {
		name    string
		usage   btrfsUsage
		want    float64
		wantErr bool
	}{
		{
			name:  "half free",
			usage: btrfsUsage{DeviceSizeBytes: 1000, FreeEstimatedBytes: 500},
			want:  50,
		},
		{
			name:  "fully used",
			usage: btrfsUsage{DeviceSizeBytes: 1000, FreeEstimatedBytes: 0},
			want:  100,
		},
		{
			name:  "empty",
			usage: btrfsUsage{DeviceSizeBytes: 1000, FreeEstimatedBytes: 1000},
			want:  0,
		},
		{
			name:    "zero device size errors",
			usage:   btrfsUsage{DeviceSizeBytes: 0, FreeEstimatedBytes: 0},
			wantErr: true,
		},
		{
			name:  "free estimate somehow exceeds size clamps to 0 used",
			usage: btrfsUsage{DeviceSizeBytes: 1000, FreeEstimatedBytes: 1500},
			want:  0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := btrfsUsagePercent(c.usage)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got pct=%v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
