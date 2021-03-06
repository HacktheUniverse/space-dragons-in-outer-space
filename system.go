package main

import (
	"database/sql"
	"fmt"
	"io"
	"math"
	"math/rand"
	"time"
)

var (
	index     map[int]*System
	nameIndex map[string]*System
)

type System struct {
	id          int
	x, y, z     float64
	planets     int
	name        string
	players     map[*Connection]bool
	miningRate  float64
	colonizedBy *Connection
}

func (s *System) Arrive(p *Connection) {
	p.SetSystem(s)
	log_info("player %s has arrived at system %s", p.PlayerName(), s.name)
	if s.players == nil {
		s.players = make(map[*Connection]bool, 8)
	}
	s.players[p] = true
}

func (s *System) Leave(p *Connection) {
	delete(s.players, p)
	p.location = nil
}

func (s *System) EachConn(fn func(*Connection)) {
	if s.players == nil {
		return
	}
	for conn, _ := range s.players {
		fn(conn)
	}
}

func (s *System) NumInhabitants() int {
	if s.players == nil {
		return 0
	}
	return len(s.players)
}

func (e System) Store(db *sql.DB) {
	_, err := db.Exec(`
    insert into planets
    (name, x, y, z, planets)
    values
    (?, ?, ?, ?, ?)
    ;`, e.name, e.x, e.y, e.z, e.planets)
	if err != nil {
		log_error("%v", err)
	}
}

func (s *System) DistanceTo(other *System) float64 {
	return dist3d(s.x, s.y, s.z, other.x, other.y, other.z)
}

func (s *System) LightTimeTo(other *System) time.Duration {
	return time.Duration(int64(s.DistanceTo(other) * 100000000))
}

func (s *System) BombTimeTo(other *System) time.Duration {
	return time.Duration(int64(s.DistanceTo(other) * 110000000))
}

func (s *System) TravelTimeTo(other *System) time.Duration {
	return time.Duration(int64(s.DistanceTo(other) * 125000000))
}

func (s *System) Bombed(bomber *Connection) {
	s.EachConn(func(conn *Connection) {
		conn.Die()
		bomber.MadeKill(conn)
	})
	if s.colonizedBy != nil {
		fmt.Fprintf(s.colonizedBy, "your mining colony on %s has been destroyed!\n", s.name)
		s.colonizedBy = nil
	}

	for id, _ := range index {
		if id == s.id {
			continue
		}
		delay := s.BombTimeTo(index[id])
		id2 := id
		After(delay, func() {
			bombNotice(id2, s.id)
		})
	}
}

func bombNotice(to_id, from_id int) {
	to := index[to_id]
	from := index[from_id]
	to.EachConn(func(conn *Connection) {
		fmt.Fprintf(conn, "a bombing has been observed on %s\n", from.name)
	})
}

func (e System) String() string {
	return fmt.Sprintf("<name: %s x: %v y: %v z: %v planets: %v>", e.name, e.x, e.y, e.z, e.planets)
}

type Neighbor struct {
	id       int
	distance float64
}

func (e *System) Nearby(n int) ([]Neighbor, error) {
	rows, err := db.Query(`
        select planets.id, edges.distance
        from edges
        join planets on edges.id_2 = planets.id
        where edges.id_1 = ?
        order by distance
        limit ?
    ;`, e.id, n)
	if err != nil {
		log_error("unable to get nearby systems for %s: %v", e.name, err)
		return nil, err
	}
	neighbors := make([]Neighbor, 0, n)
	for rows.Next() {
		var neighbor Neighbor
		if err := rows.Scan(&neighbor.id, &neighbor.distance); err != nil {
			log_error("error unpacking row from nearby neighbors query: %v", err)
			continue
		}
		neighbors = append(neighbors, neighbor)
	}
	return neighbors, nil
}

func countSystems() (int, error) {
	row := db.QueryRow(`select count(*) from planets`)

	var n int
	err := row.Scan(&n)
	return n, err
}

func sq(x float64) float64 {
	return x * x
}

func dist3d(x1, y1, z1, x2, y2, z2 float64) float64 {
	return math.Sqrt(sq(x1-x2) + sq(y1-y2) + sq(z1-z2))
}

func indexSystems() map[int]*System {
	rows, err := db.Query(`select * from planets`)
	if err != nil {
		log_error("unable to select all planets: %v", err)
		return nil
	}
	defer rows.Close()
	index = make(map[int]*System, 551)
	nameIndex = make(map[string]*System, 551)
	for rows.Next() {
		p := System{}
		if err := rows.Scan(&p.id, &p.name, &p.x, &p.y, &p.z, &p.planets); err != nil {
			log_info("unable to scan planet row: %v", err)
			continue
		}
		index[p.id] = &p
		nameIndex[p.name] = &p
		p.miningRate = rand.Float64()
	}
	return index
}

func randomSystem() (*System, error) {
	n := len(index)
	if n == 0 {
		return nil, fmt.Errorf("no planets are known to exist")
	}

	pick := rand.Intn(n)
	planet := index[pick]
	return planet, nil
}

type scanResults struct {
	life        bool
	miningRate  float64
	colonizedBy *Connection
}

func (r *scanResults) negative() bool {
	return !r.life && r.colonizedBy == nil
}

func (r *scanResults) String() string {
	if r.life {
		return "life detected"
	}
	return "(none)"
}

func (r *scanResults) write(w io.Writer) {
	if r.life {
		fmt.Fprintf(w, "\tlife detected\n")
	}
	if r.colonizedBy != nil {
		fmt.Fprintf(w, "\tmining colony owned by %s\n", r.colonizedBy.PlayerName())
	}
}

func scanSystem(id int, reply int) {
	system := index[id]
	source := index[reply]
	delay := system.LightTimeTo(source)
	log_info("scan hit %s from %s after traveling for %v", system.name, source.name, delay)

	system.EachConn(func(conn *Connection) {
		fmt.Fprintf(conn, "scan detected from %s\n", source.name)
	})
	results := &scanResults{
		life:        len(system.players) > 0,
		colonizedBy: system.colonizedBy,
	}
	After(delay, func() {
		deliverReply(source.id, system.id, results)
	})
}

func deliverReply(id int, echo int, results *scanResults) {
	system := index[id]
	source := index[echo]
	delay := system.LightTimeTo(source)
	log_info("echo received at %s reflected from %s after traveling for %v", system.name, source.name, delay)
	system.EachConn(func(conn *Connection) {
		if results.negative() {
			return
		}
		fmt.Fprintf(conn, "scan results from %s (%v away):\n", source.name, delay)
		results.write(conn)
	})
}

func deliverMessage(to_id, from_id int, msg string) {
	to := index[to_id]
	from := index[from_id]
	to.EachConn(func(conn *Connection) {
		fmt.Fprintf(conn, "Message from %s: %s", from.name, msg)
	})
}
