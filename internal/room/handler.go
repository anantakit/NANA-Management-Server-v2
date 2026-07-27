package room

import (
	"nana/internal/shared/bind"
	"nana/internal/shared/pagination"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type RoomHandler struct {
	svc RoomService
}

func NewRoomHandler(svc RoomService) *RoomHandler {
	return &RoomHandler{svc: svc}
}

func (h *RoomHandler) RegisterRoutes(router fiber.Router) {
	router.Get("/", h.List)
	router.Post("/", h.Create)
	router.Get("/:roomId", h.GetByID)
	router.Put("/:roomId", h.Update)
	router.Delete("/:roomId", h.Delete)
}

// RegisterLookupRoutes registers the room-addressed READ boundary at
// /rooms/:roomId. It is a pure re-addressing of the existing read: the service
// method `GetByID` already takes only a room id, and the apartment-nested
// handler below uses the apartment id solely as a membership guard. No new
// query, no new read model.
//
// Room-scoped surfaces (Meter Continuity, /rooms/:roomId/meter) carry only a
// stable roomId in their route — the apartment is resolved data, not navigation
// identity — so they need the room addressable by its own id.
func (h *RoomHandler) RegisterLookupRoutes(router fiber.Router) {
	router.Get("/:roomId", h.GetByRoomID)
}

// GetByRoomID resolves a room (plus its active-contract summary) from the room
// id alone. Same service call as GetByID, minus the apartment-membership guard
// that only exists because the nested route carries an apartment id.
func (h *RoomHandler) GetByRoomID(c fiber.Ctx) error {
	roomID, err := uuid.Parse(c.Params("roomId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสห้องไม่ถูกต้อง"})
	}

	room, err := h.svc.GetByID(c.Context(), roomID)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "สำเร็จ", ToRoomWithContractResponseList([]RoomWithContract{*room})[0])
}

func (h *RoomHandler) List(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var params pagination.PaginationParams
	if err := bind.Query(c, &params); err != nil {
		return err
	}
	params.Normalize()

	rooms, total, err := h.svc.ListByApartment(c.Context(), apartmentID, params)
	if err != nil {
		return respond.Error(c, err)
	}

	meta := pagination.ComputeMeta(params.Page, params.Limit, total)
	return respond.SuccessWithMeta(c, "สำเร็จ", ToRoomWithContractResponseList(rooms), meta)
}

func (h *RoomHandler) GetByID(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}
	roomID, err := uuid.Parse(c.Params("roomId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสห้องไม่ถูกต้อง"})
	}

	room, err := h.svc.GetByID(c.Context(), roomID)
	if err != nil {
		return respond.Error(c, err)
	}
	if room.ApartmentID != apartmentID {
		return respond.Error(c, respond.ErrNotFound.WithMessage("ไม่พบห้องในอาคารนี้"))
	}
	return respond.Success(c, "สำเร็จ", ToRoomWithContractResponseList([]RoomWithContract{*room})[0])
}

func (h *RoomHandler) Create(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}

	var req CreateRoomRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	room, err := h.svc.Create(c.Context(), apartmentID, req)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Created(c, "สร้างห้องแล้ว", ToRoomResponse(*room))
}

func (h *RoomHandler) Update(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}
	roomID, err := uuid.Parse(c.Params("roomId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสห้องไม่ถูกต้อง"})
	}

	var req UpdateRoomRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	room, err := h.svc.Update(c.Context(), roomID, req)
	if err != nil {
		return respond.Error(c, err)
	}
	if room.ApartmentID != apartmentID {
		return respond.Error(c, respond.ErrNotFound.WithMessage("ไม่พบห้องในอาคารนี้"))
	}
	return respond.Success(c, "อัปเดตห้องแล้ว", ToRoomResponse(*room))
}

func (h *RoomHandler) Delete(c fiber.Ctx) error {
	apartmentID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสอาคารไม่ถูกต้อง"})
	}
	roomID, err := uuid.Parse(c.Params("roomId"))
	if err != nil {
		return respond.ValidationError(c, []string{"รหัสห้องไม่ถูกต้อง"})
	}

	// Verify room belongs to apartment
	room, err := h.svc.GetByID(c.Context(), roomID)
	if err != nil {
		return respond.Error(c, err)
	}
	if room.ApartmentID != apartmentID {
		return respond.Error(c, respond.ErrNotFound.WithMessage("ไม่พบห้องในอาคารนี้"))
	}

	if err := h.svc.Delete(c.Context(), roomID); err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "ลบห้องแล้ว", nil)
}
