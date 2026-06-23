// Package calendar define el modelo de una clase. Los datos se obtienen vía el
// paquete vapi (API móvil JSON de Virgin); aquí solo vive el tipo Class y el
// mapeo de clubes.
package calendar

// Clubes (clubId numérico de vapi).
const (
	ClubCorsoComo = 209
	ClubCavour    = 224
)

// ClubNames mapea clubId → nombre legible.
var ClubNames = map[int]string{
	ClubCorsoComo: "Milano Corso Como",
	ClubCavour:    "Milano Cavour",
}

// Class es una clase programada en el calendario.
type Class struct {
	Date       string `json:"date"`  // YYYY-MM-DD
	Start      string `json:"start"` // HH:MM
	End        string `json:"end"`   // HH:MM
	Name       string `json:"name"`  // p. ej. "Calisthenics Performance"
	Club       string `json:"club"`  // nombre legible del club
	Studio     string `json:"studio"`
	Instructor string `json:"instructor"` // p. ej. "Alessandro Restelli"
	ClubID     int    `json:"clubId"`     // 209 / 224
	ClassID    int    `json:"classId"`    // id de la clase (para reservar)
	SessionID  int    `json:"sessionId"`  // id de la sesión (para reservar/cancelar)
	BookingID  int    `json:"bookingId"`  // id de la reserva del socio (para cancelar), solo si Booked
	Booked     bool   `json:"booked"`     // true si ya está reservada por mí
	// Status: "bookable" (reservable ya), "booked" (reservada por mí),
	// "waitlist" (llena, lista de espera), "unavailable" (plazo no abierto/otro).
	Status string `json:"status"`
}
