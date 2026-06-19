package monthly

import (
	"nana/internal/billing"
	"nana/internal/shared/bind"
	"nana/internal/shared/middleware"
	"nana/internal/shared/pagination"
	"nana/internal/shared/respond"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the monthly billing workflow's 9 routes under the
// caller-supplied router (typically admin.Group("/bills")). Routes are
// registered in literal-before-param order so /batches/* and /preflight /
// /batch-monthly / /finalize-all-by-month all win over /:id at the radix
// match level — matches the pre-extraction registration order in
// billing/handler.go to preserve identical path semantics.
//
// MUST be mounted BEFORE billing.Handler.RegisterRoutes so the literal
// segments are registered first.
func (h *Handler) RegisterRoutes(r fiber.Router) {
	// Batch routes (literal /batches/... beats /:id at the matcher level).
	r.Get("/batches", h.ListBatches)
	r.Get("/batches/:id", h.GetBatch)
	r.Get("/batches/:id/items", h.GetBatchItems)
	r.Post("/batches/:id/commit", h.CommitBatch)
	r.Post("/batches/:id/finalize-all", h.BatchFinalizeAll)
	r.Post("/batches/:id/items/:itemId/replan", h.RePlanBatchItem)

	// Root-level monthly workflow routes (literals — registered before /:id).
	r.Get("/preflight", h.PreflightMonthly)
	r.Post("/batch-monthly", h.BatchCreateMonthly)
	r.Post("/finalize-all-by-month", h.FinalizeAllByMonth)
}

// --- Batch listing ---

func (h *Handler) ListBatches(c fiber.Ctx) error {
	var params billing.BatchListParams
	if err := bind.Query(c, &params); err != nil {
		return err
	}
	batches, total, err := h.svc.ListBatches(c.Context(), params)
	if err != nil {
		return respond.Error(c, err)
	}
	resp := make([]BatchHeaderResponse, len(batches))
	for i := range batches {
		resp[i] = ToBatchHeaderResponse(&batches[i])
	}
	meta := pagination.ComputeMeta(params.Page, params.Limit, total)
	return respond.SuccessWithMeta(c, "สำเร็จ", resp, meta)
}

// --- Single batch read ---

func (h *Handler) GetBatch(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}
	batch, err := h.svc.GetBatchByID(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "สำเร็จ", ToBatchHeaderResponse(batch))
}

func (h *Handler) GetBatchItems(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}
	items, err := h.svc.GetBatchItems(c.Context(), id)
	if err != nil {
		return respond.Error(c, err)
	}
	resp := make([]BatchItemResponse, len(items))
	for i, it := range items {
		resp[i] = ToBatchItemResponse(it)
	}
	return respond.Success(c, "สำเร็จ", resp)
}

// --- Batch commit ---

func (h *Handler) CommitBatch(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}

	result, err := h.svc.CommitBatch(c.Context(), id)
	if err != nil {
		// Partial commit: return 200 with result so FE can see progress.
		if result != nil {
			return respond.Success(c, "สร้างบิลร่างไม่สำเร็จบางส่วน กรุณาลองใหม่", ToCommitBatchResponse(result))
		}
		return respond.Error(c, err)
	}

	return respond.Success(c, "สร้างบิลร่างแล้ว", ToCommitBatchResponse(result))
}

// --- Batch finalize-all ---

// BatchFinalizeAll finalizes every DRAFT monthly bill in the given batch.
// Returns 200 + structured result body (success_count / fail_count /
// failures[]) — partial failure is a normal outcome, not an HTTP error,
// so the FE can render per-row reasons without parsing a 4xx body.
func (h *Handler) BatchFinalizeAll(c fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}

	result, err := h.svc.BatchFinalizeAll(c.Context(), id, middleware.ActorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	msg := "ออกบิลแล้ว"
	if result.FailCount > 0 {
		msg = "ออกบิลบางใบไม่สำเร็จ กรุณาตรวจสอบ"
	}
	return respond.Success(c, msg, result)
}

// --- Per-item replan ---

// RePlanBatchItem re-evaluates a single batch item against current state
// (e.g. after recording the missing meter for a SKIPPED row) and rewrites
// its classification + snapshot. POST so the side effect is explicit in
// the HTTP verb. Idempotent — calling repeatedly with no state change
// returns the same classification.
func (h *Handler) RePlanBatchItem(c fiber.Ctx) error {
	batchID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("id ไม่ถูกต้อง"))
	}
	itemID, err := uuid.Parse(c.Params("itemId"))
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("item_id ไม่ถูกต้อง"))
	}
	item, err := h.svc.RePlanBatchItem(c.Context(), batchID, itemID)
	if err != nil {
		return respond.Error(c, err)
	}
	return respond.Success(c, "สำเร็จ", ToBatchItemResponse(*item))
}

// --- Preflight ---

func (h *Handler) PreflightMonthly(c fiber.Ctx) error {
	var req MonthlyPreflightRequest
	if err := bind.Query(c, &req); err != nil {
		return err
	}

	result, err := h.svc.PreflightMonthly(c.Context(), req)
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "สำเร็จ", ToMonthlyPreflightResponse(result))
}

// --- Batch trigger (POST /batch-monthly) ---

func (h *Handler) BatchCreateMonthly(c fiber.Ctx) error {
	var req BatchCreateMonthlyBillsRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}

	result, err := h.svc.BatchCreateMonthlyBills(c.Context(), req, middleware.ActorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	return respond.Success(c, "คำนวณรอบบิลรายเดือนแล้ว", ToBatchTriggerResponse(result))
}

// --- FinalizeAllByMonth ---

// FinalizeAllByMonth bulk-finalizes every DRAFT monthly bill scoped to
// (apartment, billing_month). Per-month sibling of BatchFinalizeAll for
// bills created via the reconciliation Generate path (no Batch entity).
// Same response shape so the FE can reuse FinalizeAllModal verbatim;
// partial failure is a 200 with failures[] populated, not an HTTP error.
func (h *Handler) FinalizeAllByMonth(c fiber.Ctx) error {
	var req FinalizeAllByMonthRequest
	if err := bind.Body(c, &req); err != nil {
		return err
	}
	apartmentID, err := uuid.Parse(req.ApartmentID)
	if err != nil {
		return respond.Error(c, respond.ErrBadRequest.WithMessage("apartment_id ไม่ถูกต้อง"))
	}

	result, err := h.svc.FinalizeAllByMonth(c.Context(), apartmentID, req.BillingMonth, middleware.ActorFromCtx(c))
	if err != nil {
		return respond.Error(c, err)
	}

	msg := "ออกบิลแล้ว"
	if result.FailCount > 0 {
		msg = "ออกบิลบางใบไม่สำเร็จ กรุณาตรวจสอบ"
	}
	return respond.Success(c, msg, result)
}
