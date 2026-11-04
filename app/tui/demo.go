package tui

import (
	"time"

	"github.com/dustinevan/scrunchie/lib/data/postgres"
	"github.com/dustinevan/scrunchie/pkg/result"
)

const demoSQL = `-- Totally offline demo: no database, no network, no parents.
-- Ctrl+J or F5 runs it. Enter on a cell opens its diary (JSON)
-- or mixtape (bytea). Outside SQL, ? shows what you can do.
SELECT id, activity, diary, mixtape
FROM rad.sleepover;
`

// initDemo opens one offline pseudo-connection with a sample catalog. It has
// no session; Run returns demoBatch.
func (m *Model) initDemo() {
	if m.config.InitialSQL == "" {
		m.buffers[0].editor = newEditor(demoSQL)
		m.buffers[0].saved = demoSQL
	}
	relation := postgres.Relation{OID: 1, Schema: "rad", Name: "sleepover", Kind: "table"}
	m.nextConnection++
	c := &connection{id: m.nextConnection, name: "Demo", description: "offline demo", connected: true, txStatus: 'I'}
	c.catalog = []catalogNode{
		{kind: "schema", name: "rad", schema: "rad", parent: -1, children: []int{1}, expanded: true, loaded: true},
		{kind: "relation", name: "sleepover", schema: "rad", relation: relation, parent: 0, children: []int{2, 3, 4, 5}, expanded: true, loaded: true},
		{kind: "column", name: "id", schema: "rad", relation: relation, detail: "int8 PK NOT NULL", parent: 1, loaded: true},
		{kind: "column", name: "activity", schema: "rad", relation: relation, detail: "text", parent: 1, loaded: true},
		{kind: "column", name: "diary", schema: "rad", relation: relation, detail: "jsonb", parent: 1, loaded: true},
		{kind: "column", name: "mixtape", schema: "rad", relation: relation, detail: "bytea", parent: 1, loaded: true},
	}
	m.connections = append(m.connections, c)
	m.buffers[0].conn = c.id
	m.buffers[0].results.batch = demoBatch()
	m.buffers[0].results.conn, m.buffers[0].results.connName = c.id, c.name
	m.status = "Offline demo · Ctrl+J runs · ↓ at the end of SQL reaches results · outside SQL, ? shows what you can do"
}

// demoBatch covers the display edge cases: a number beyond float precision,
// nested and empty JSON containers, JSON null, control characters, SQL NULL,
// empty text and bytes, and a bytea that is also a valid UUID.
func demoBatch() result.Batch {
	return result.Batch{
		Duration: 3 * time.Millisecond, TxStatus: 'I',
		Results: []result.Set{{
			Columns: []result.Column{
				{Name: "id", Type: "int8", OID: 20},
				{Name: "activity", Type: "text", OID: 25},
				{Name: "diary", Type: "jsonb", OID: 3802},
				{Name: "mixtape", Type: "bytea", OID: 17},
			},
			Rows: [][]result.Cell{
				{{Text: "101"}, {Text: "slumber party"}, {Text: `{"host":"Tiffany","year":1987,"rsvp":true,"basement":{"lights":"black light","boombox":{"volume":11,"batteries":4}},"games":[{"name":"truth or dare","fun":true},{"name":"prank calls","fun":false}],"glitter_particles":123456789012345678901234567890}`}, {Bytes: []byte{0x55, 0x0e, 0x84, 0x00, 0xe2, 0x9b, 0x41, 0xd4, 0xa7, 0x16, 0x44, 0x66, 0x55, 0x44, 0x00, 0x00}}},
				{{Text: "102"}, {Text: "friendship bracelets"}, {Text: `{"maker":{"name":"Jessica","colors":["hot pink","electric blue"],"style":{"knot":"chevron","charms":null}},"secrets":{},"pen_pals":[],"crush":null}`}, {Bytes: []byte(`{"side":"A","rewound":true}`)}},
				{{Text: "103"}, {Text: "passing notes"}, {Text: `{"note":"meet at the roller rink\nafter school","fold":"triangle\tstyle","escape":"\u001b[31mshown safely, never as red"}`}, {Bytes: []byte{0, 9, 10, 13, 27, 0x7f, 0xff}}},
				{{Text: "104"}, {Text: "grounded"}, {Null: true}, {Null: true}},
				{{Text: "105"}, {Text: "blank tape"}, {Text: `{"lyrics":"","rewinds":0,"cool":false}`}, {Bytes: []byte{}}},
			},
			Command: "SELECT 5", RowCount: 5,
		}},
	}
}
