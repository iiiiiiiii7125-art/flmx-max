package connlog

import "testing"

func TestParseServiceMark(t *testing.T) {
	cases := []struct {
		name    string
		service string
		md      map[string]any
		want    Mark
		wantOK  bool
	}{
		{
			name:    "panel shape",
			service: "1_2_0_tcp",
			md:      map[string]any{"connlogEntryKind": "forward", "connlogEntryID": "1_2_0_tcp"},
			want:    Mark{Kind: "forward", ID: "1_2_0_tcp"},
			wantOK:  true,
		},
		{
			name:    "viper lowercased keys",
			service: "1_2_0_udp",
			md:      map[string]any{"connlogentrykind": "forward", "connlogentryid": "1_2_0_udp"},
			want:    Mark{Kind: "forward", ID: "1_2_0_udp"},
			wantOK:  true,
		},
		{
			name:    "id falls back to service name",
			service: "3_4_5_tcp",
			md:      map[string]any{"connlogEntryKind": "forward"},
			want:    Mark{Kind: "forward", ID: "3_4_5_tcp"},
			wantOK:  true,
		},
		{
			name:    "blank id falls back to service name",
			service: "3_4_5_udp",
			md:      map[string]any{"connlogEntryKind": "forward", "connlogEntryID": "  "},
			want:    Mark{Kind: "forward", ID: "3_4_5_udp"},
			wantOK:  true,
		},
		{
			name:    "no metadata",
			service: "1_2_0_tcp",
			md:      nil,
			wantOK:  false,
		},
		{
			name:    "unrelated metadata only",
			service: "1_2_0_tcp",
			md:      map[string]any{"idleTimeout": 300},
			wantOK:  false,
		},
		{
			name:    "unknown kind rejected",
			service: "1_2_0_tcp",
			md:      map[string]any{"connlogEntryKind": "inbound", "connlogEntryID": "in-7"},
			wantOK:  false,
		},
		{
			name:    "non-string kind rejected",
			service: "1_2_0_tcp",
			md:      map[string]any{"connlogEntryKind": 1, "connlogEntryID": "1_2_0_tcp"},
			wantOK:  false,
		},
		{
			name:    "kind without any usable id",
			service: "",
			md:      map[string]any{"connlogEntryKind": "forward"},
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseServiceMark(tc.service, tc.md)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("ParseServiceMark(%q, %v) = (%v, %v), want (%v, %v)",
					tc.service, tc.md, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestPublishKernelEntriesReplacesPerType(t *testing.T) {
	PublishKernelEntries("xray", []KernelEntry{{InboundID: 7, Tag: "in-7"}, {InboundID: 8, Tag: "in-8"}})
	PublishKernelEntries("singbox", []KernelEntry{{InboundID: 9, Tag: "in-9"}})

	if got := KernelEntries("xray"); len(got) != 2 {
		t.Fatalf("xray entries = %v, want 2", got)
	}
	mark, ok := KernelMark("xray", "in-8")
	if !ok || mark != (Mark{Kind: "inbound", ID: "in-8"}) {
		t.Fatalf("KernelMark(xray, in-8) = (%v, %v)", mark, ok)
	}
	if _, ok := KernelMark("singbox", "in-8"); ok {
		t.Fatal("tag leaked across kernel types")
	}

	// Re-apply without in-7 (its relay was removed / inbound became a
	// landing): the new set replaces the old one wholesale.
	PublishKernelEntries("xray", []KernelEntry{{InboundID: 8, Tag: "in-8"}})
	if _, ok := KernelMark("xray", "in-7"); ok {
		t.Fatal("stale kernel entry survived republish")
	}
	// Other kernel types are untouched by the republish.
	if got := KernelEntries("singbox"); len(got) != 1 {
		t.Fatalf("singbox entries = %v, want 1", got)
	}

	// Blank tags are dropped; an empty publish clears the type.
	PublishKernelEntries("xray", []KernelEntry{{InboundID: 10, Tag: " "}})
	if got := KernelEntries("xray"); len(got) != 0 {
		t.Fatalf("xray entries after blank-tag publish = %v, want empty", got)
	}
}
