package health

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// ASNs are a snapshot of RIPEstat inverse org lookups on 2026-10-26:
// rest.db.ripe.net/search.json?inverse-attribute=org&type-filter=aut-num&query-string=<ORG>
// MTS: ORG-ZM1-RIPE; Beeline: ORG-ES15-RIPE plus AS8402 and AS16345; MegaFon: ORG-OM1-RIPE;
// Rostelecom: ORG-JR8-RIPE; T2: ORG-MCC1-RIPE; ER-Telecom: ORG-RA21-RIPE.
var countryOperators = map[string][]countryOperator{
	"RU": {
		{Name: "MTS", ASNs: []uint32{
			13055, 13062, 13155, 13174, 15640, 16012, 16256, 197023, 197070, 198618, 199054, 20866,
			209024, 21103, 21365, 25086, 28832, 28884, 29190, 29194, 29209, 29225, 29497, 30751,
			30881, 30922, 31286, 31338, 31558, 33894, 34351, 34508, 35473, 35728, 38996, 39001,
			39799, 39811, 39858, 40993, 41145, 41209, 41279, 41771, 41822, 41929, 42087, 42115,
			42322, 43038, 43148, 43218, 43318, 44257, 44579, 44677, 44731, 44736, 44895, 44987,
			45048, 47579, 47899, 48000, 48100, 48123, 48124, 48176, 48212, 48322, 48400, 48541,
			48612, 48788, 48796, 49154, 49170, 49350, 49665, 49816, 50011, 50071, 50240, 50397,
			50406, 50646, 51116, 56761, 60490, 60496, 60891, 62347, 62348, 6731, 8359, 8580,
		}},
		{Name: "Beeline", ASNs: []uint32{
			12543, 13095, 13257, 16043, 20533, 20597, 21332, 21480, 21483, 2599, 2766, 28703,
			29125, 31359, 31425, 3216, 3235, 3253, 34038, 34747, 34894, 42110, 42245, 42842,
			43275, 43687, 43970, 49144, 8350, 8371, 8563, 8755, 8773, 8402, 16345,
		}},
		{Name: "MegaFon", ASNs: []uint32{
			12396, 12714, 13075, 15781, 202804, 20632, 20663, 24767, 24866, 25159, 25290, 29648,
			31088, 31133, 31163, 31195, 31205, 31208, 31213, 31224, 31261, 31268, 34552, 35298,
			41976, 42771, 42891, 43583, 43841, 47218, 47395, 48615, 50315, 50928, 51638, 56768,
			60773, 6850, 6854, 8263,
		}},
		{Name: "Rostelecom", ASNs: []uint32{
			12332, 12380, 12389, 12683, 12685, 12705, 12730, 12846, 13056, 13118, 15500, 15759,
			15934, 16054, 16287, 16301, 196747, 204354, 20519, 21017, 21289, 21378, 21479, 21487,
			24612, 24665, 24699, 24783, 24789, 24810, 24873, 25008, 25405, 25436, 25490, 25510,
			25515, 25531, 28719, 2878, 28802, 28860, 29069, 29456, 30749, 30974, 31094, 31185,
			31353, 31496, 3239, 33887, 33934, 34137, 34168, 34205, 34267, 34297, 34449, 34490,
			34584, 34875, 34892, 34974, 35078, 35125, 35154, 35177, 35389, 35400, 35516, 35531,
			38951, 38976, 38985, 39054, 39229, 39407, 41134, 41190, 41440, 41691, 42091, 42358,
			42362, 42448, 42548, 42610, 42825, 43053, 43132, 43574, 43793, 43975, 44237, 44412,
			44467, 44982, 47460, 48421, 51281, 5573, 5591, 56341, 56350, 6828, 6844, 6863,
			8249, 8382, 8439, 8443, 8557, 8568, 8570, 8594, 8675, 8828, 8997, 15468,
			43468,
		}},
		{Name: "T2", ASNs: []uint32{
			12958, 15378, 34287, 41330, 41592, 42437, 48092, 48190, 39374,
		}},
		{Name: "ER-Telecom", ASNs: []uint32{
			12690, 12768, 12772, 13094, 13230, 15599, 15756, 16179, 16300, 197140, 197235, 197585,
			197673, 198295, 199253, 201495, 204952, 205971, 206661, 20730, 20807, 208786, 210766, 21311,
			21335, 21353, 21442, 21447, 24588, 25227, 25408, 25446, 25549, 29053, 30868, 31036,
			31302, 31363, 31483, 31484, 31494, 31585, 31692, 33899, 34145, 34150, 34533, 34590,
			34925, 35374, 39028, 39435, 41002, 41403, 41661, 41668, 41682, 41727, 41733, 41754,
			41786, 41843, 41854, 42113, 42116, 42145, 42218, 42368, 42631, 42682, 42683, 43097,
			43154, 43314, 43478, 43550, 43675, 44053, 44727, 44811, 44905, 45051, 47111, 47151,
			47398, 47659, 47911, 48201, 48532, 48858, 49048, 49874, 50498, 50512, 50542, 50543,
			50544, 51034, 51035, 51118, 51178, 51570, 51604, 51645, 51819, 52207, 5563, 56330,
			56377, 56384, 56420, 56969, 56981, 57026, 57041, 57044, 57079, 57378, 57620, 58157,
			58189, 58344, 59713, 60043, 60430, 62287, 62399, 62423, 8331, 8345, 8711, 9049,
		}},
	},
}

type countryOperator struct {
	Name string
	ASNs []uint32
}

// countryWants returns the operator order with never-asked operators first, then the least recently asked. It only
// considers rounds from the requested country; the stable sort keeps the table order for ties.
func countryWants(country string, rows []store.CountryCheck) []CountryWant {
	operators := countryOperators[country]
	if len(operators) == 0 {
		return nil
	}
	var newest time.Time
	for _, row := range rows {
		if row.Country == country && row.Verdict != verdictUnknown && row.At.After(newest) {
			newest = row.At
		}
	}

	asked := make([]bool, len(operators))
	lastAsked := make([]time.Time, len(operators))
	var suspects []CountryWant
	seenSuspects := map[struct {
		asn  uint32
		city string
	}]bool{}
	for _, row := range rows {
		if row.Country != country || row.Detail == "" {
			continue
		}
		var detail countryDetail
		if json.Unmarshal([]byte(row.Detail), &detail) != nil {
			continue
		}
		markAsked := func(i int) {
			if !asked[i] || row.At.After(lastAsked[i]) {
				asked[i] = true
				lastAsked[i] = row.At
			}
		}
		for _, name := range detail.Asked {
			for i, operator := range operators {
				if operator.Name == name {
					markAsked(i)
					break
				}
			}
		}
		for _, probe := range detail.Probes {
			for i, operator := range operators {
				if slices.Contains(operator.ASNs, probe.ASN) {
					markAsked(i)
				}
			}
			if row.Verdict == verdictPartial && row.At.Equal(newest) && probe.Dark && probe.Valid && probe.Eyeball && probe.ASN != 0 {
				key := struct {
					asn  uint32
					city string
				}{probe.ASN, probe.City}
				if !seenSuspects[key] {
					name := probe.Network // the table's name when the ASN is an operator's, so the source dedupes and counts it as asked
					for _, operator := range operators {
						if slices.Contains(operator.ASNs, probe.ASN) {
							name = operator.Name
							break
						}
					}
					suspects = append(suspects, CountryWant{Name: name, ASNs: []uint32{probe.ASN}, City: probe.City})
					seenSuspects[key] = true
				}
			}
		}
	}

	order := make([]int, len(operators))
	for i := range operators {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if asked[a] != asked[b] {
			return !asked[a]
		}
		if asked[a] && !lastAsked[a].Equal(lastAsked[b]) {
			return lastAsked[a].Before(lastAsked[b])
		}
		return a < b
	})

	wants := make([]CountryWant, 0, len(suspects)+len(order))
	wants = append(wants, suspects...)
	for _, i := range order {
		wants = append(wants, CountryWant{Name: operators[i].Name, ASNs: slices.Clone(operators[i].ASNs)})
	}
	return wants
}

func (s *Service) countryRoundWants(ctx context.Context, country string) []CountryWant {
	tableOrder := countryWants(country, nil)
	if len(tableOrder) == 0 {
		return nil
	}
	rows, err := s.st.RecentCountryChecks(ctx, familyV4, countryRecent)
	if err != nil {
		s.log.Warn("health: country check operator rotation", "err", err)
		return tableOrder
	}
	return countryWants(country, rows)
}
