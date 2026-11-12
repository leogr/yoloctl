// yoloctl's private ABI over a pinned libghostty-vt revision. This file is
// imported by lib_vt.zig at artifact build time; it contains no VT parser.
const std = @import("std");
const c = @import("terminal/c/main.zig");
const formatter = @import("terminal/formatter.zig");
const Selection = @import("terminal/Selection.zig");
const lib = @import("terminal/lib.zig");
const color = @import("terminal/color.zig");
const input_key = @import("input/key.zig");
const key_encode = @import("input/key_encode.zig");
const modes = @import("terminal/modes.zig");
const kitty_key = @import("terminal/kitty/key.zig");
const point = @import("terminal/point.zig");
const tracked = @import("terminal/c/grid_ref_tracked.zig");
const input_mouse = @import("input/mouse.zig");
const mouse_encode = @import("input/mouse_encode.zig");
const SelectionGesture = @import("terminal/SelectionGesture.zig");
const selection_codepoints = @import("terminal/selection_codepoints.zig");
const stream = @import("terminal/stream.zig");
const snapshot_result = @import("terminal/c/result.zig").Result;
const size_report = @import("terminal/size_report.zig");

const continuation_limit: usize = 32 * 1024;
const history_bytes: usize = 64 * 1024 * 1024;

extern "yolo_vt" fn reply(ptr: [*]const u8, len: usize) void;
extern "yolo_vt" fn row(index: u32, ptr: [*]const u8, len: usize, selected_start: u32, selected_end: u32) void;
extern "yolo_vt" fn event(kind: u32, value: u32) void;

fn writeReply(_: c.terminal.Terminal, _: ?*anyopaque, ptr: [*]const u8, len: usize) callconv(.c) void {
    reply(ptr, len);
}
fn bell(_: c.terminal.Terminal, _: ?*anyopaque) callconv(.c) void {
    event(1, 0);
}
fn hold(_: c.terminal.Terminal, _: ?*anyopaque, value: bool) callconv(.c) void {
    event(2, @intFromBool(value));
}
fn reset(_: c.terminal.Terminal, _: ?*anyopaque) callconv(.c) void {
    event(3, 0);
}
fn size(t: c.terminal.Terminal, _: ?*anyopaque, out: *size_report.Size) callconv(.c) bool {
    const v = c.terminal.zigTerminal(t) orelse return false;
    out.* = .{
        .rows = v.rows, .columns = v.cols,
        .cell_width = v.width_px / v.cols,
        .cell_height = v.height_px / v.rows,
    };
    return true;
}

export fn yolo_vt_abi() u32 { return 7; }

// Callbacks and the advertised identity are local to a C terminal wrapper;
// the native snapshot deliberately does not serialize function pointers.
// Do not apply constructor defaults here: restored modes/colors/cursors win.
fn installCallbacks(t: c.terminal.Terminal) bool {
    if (c.terminal_set(t, .write_pty, @ptrCast(&writeReply)) != .success) return false;
    if (c.terminal_set(t, .bell, @ptrCast(&bell)) != .success) return false;
    if (c.terminal_set(t, .render_hold, @ptrCast(&hold)) != .success) return false;
    if (c.terminal_set(t, .reset, @ptrCast(&reset)) != .success) return false;
    if (c.terminal_set(t, .size_cb, @ptrCast(&size)) != .success) return false;
    const name = lib.String.init(@as([]const u8, "xterm-256color"));
    if (c.terminal_set(t, .terminfo_name, &name) != .success) return false;
    return true;
}

export fn yolo_vt_new(cols: u16, rows: u16, history: usize) c.terminal.Terminal {
    var t: c.terminal.Terminal = null;
    if (c.terminal_new(null, &t, cols, rows) != .success) return null;
    var success = false;
    defer if (!success) c.terminal_free(t);
    if (!installCallbacks(t)) return null;
    if (c.terminal_set(t, .continuation_max_bytes, &continuation_limit) != .success) return null;
    if (c.terminal_set(t, .scrollback_max_bytes, &history_bytes) != .success) return null;
    if (c.terminal_set(t, .scrollback_max_lines, &history) != .success) return null;
    // Preserve the frontend's default blinking block cursor; explicit child
    // DECSCUSR/mode changes override it and are carried in display frames.
    const blink = true;
    if (c.terminal_set(t, .default_cursor_blink, &blink) != .success) return null;
    // Grapheme clusters, including ZWJ emoji, occupy their composed width.
    // A default (rather than an injected sequence) survives RIS. Applications
    // may explicitly select their own mode with DECSET/DECRST 2027.
    const graphemes: c.terminal.ModeConfig = .{ .mode = 2027, .value = true };
    if (c.terminal_set(t, .mode_default, &graphemes) != .success) return null;
    if (c.terminal_set(t, .mode, &graphemes) != .success) return null;
    success = true;
    return t;
}

// Restore into a new C terminal, with no visible side effects. The caller owns
// the surrounding WASM instance and publishes it only after complete success.
// The native decoder validates framing, CRCs, records and parser continuation.
export fn yolo_vt_snapshot_restore(data: [*]const u8, len: usize, history: usize, out: *c.terminal.Terminal) i32 {
    out.* = null;
    var decoder: c.snapshot.Decoder = null;
    var status = c.snapshot.decoder_new_buf(null, &decoder, data, len);
    if (status != .success) return @intFromEnum(status);
    defer c.snapshot.decoder_free(decoder);
    status = c.snapshot.decoder_set(decoder, .max_continuation_bytes, &continuation_limit);
    if (status != .success) return @intFromEnum(status);
    const retain = true;
    status = c.snapshot.decoder_set(decoder, .retain_continuation, &retain);
    if (status != .success) return @intFromEnum(status);

    var t: c.terminal.Terminal = null;
    status = c.snapshot.decoder_ready(decoder, &t);
    if (status != .success) return @intFromEnum(status);
    var success = false;
    defer if (!success) c.terminal_free(t);
    const v = c.terminal.zigTerminal(t) orelse return @intFromEnum(snapshot_result.invalid_value);
    // Match the Go constructor's geometry limits before loading history, and
    // reject a snapshot whose declared retention policy exceeds our own.
    if (v.cols == 0 or v.rows == 0 or v.cols > 4096 or v.rows > 4096 or
        @as(usize, v.cols) * v.rows > 1024 * 1024) return @intFromEnum(snapshot_result.limit_exceeded);
    if ((v.width_px == 0) != (v.height_px == 0) or
        v.width_px % v.cols != 0 or v.height_px % v.rows != 0 or
        v.width_px / v.cols > 4096 or v.height_px / v.rows > 4096)
        return @intFromEnum(snapshot_result.invalid_value);
    const primary = v.screens.get(.primary).?;
    if (primary.pages.limits.bytes.explicit > history_bytes or
        primary.pages.limits.lines.explicit != history) return @intFromEnum(snapshot_result.limit_exceeded);
    while (true) {
        status = c.snapshot.decoder_next(decoder);
        if (status == .no_value) break;
        if (status != .success) return @intFromEnum(status);
    }
    var consumed: usize = 0;
    status = c.snapshot.decoder_get(decoder, .source_offset, &consumed);
    if (status != .success) return @intFromEnum(status);
    if (consumed != len) return @intFromEnum(snapshot_result.invalid_value);
    if (!installCallbacks(t)) return @intFromEnum(snapshot_result.invalid_value);
    out.* = t;
    success = true;
    return @intFromEnum(snapshot_result.success);
}

// Scalar state only. The private ABI deliberately does not expose Zig layouts.
export fn yolo_vt_get(t: c.terminal.Terminal, key: u32) u32 {
    const v = c.terminal.zigTerminal(t) orelse return 0;
    return switch (key) {
        1 => v.cols,
        2 => v.rows,
        3 => v.screens.active.cursor.x,
        4 => v.screens.active.cursor.y,
        5 => @intFromBool(v.screens.active.cursor.pending_wrap),
        6 => @intCast(@intFromEnum(v.screens.active_key)),
        7 => @intFromBool(v.modes.get(.cursor_visible)),
        8 => @as(u5, @bitCast(v.screens.active.kitty_keyboard.current())),
        9 => @intCast(v.screens.active.pages.total_rows - v.rows),
        10 => @intFromEnum(v.screens.active.cursor.cursor_style),
        11 => @intFromBool(v.modes.get(.cursor_blinking)),
        12 => v.scrolling_region.top,
        13 => v.scrolling_region.bottom,
        14 => @intCast(@intFromEnum(v.flags.mouse_event)),
        15 => @intCast(@intFromEnum(v.flags.mouse_format)),
        16 => @intCast(@intFromEnum(v.flags.mouse_shift_capture)),
        17 => v.width_px / v.cols,
        18 => v.height_px / v.rows,
        else => 0,
    };
}

export fn yolo_vt_mode(t: c.terminal.Terminal, mode: u16) u32 {
    var value: c.terminal.ModeConfig = .{ .mode = mode, .value = false };
    if (c.terminal_get(t, .mode, &value) != .success) return 0;
    return @intFromBool(value.value);
}

export fn yolo_vt_color(t: c.terminal.Terminal, slot: u32, rgb: u32) void {
    const value: color.RGB.C = .{ .r = @truncate(rgb >> 16), .g = @truncate(rgb >> 8), .b = @truncate(rgb) };
    _ = c.terminal_set(t, if (slot == 0) .color_foreground else .color_background, if (rgb >> 24 != 0) &value else null);
}

// Host measurements change defaults, preserving application OSC 4 overrides.
export fn yolo_vt_palette_color(t: c.terminal.Terminal, index: u8, rgb: u32) u32 {
    const v = c.terminal.zigTerminal(t) orelse return 1;
    var palette = v.colors.palette.original.*;
    palette[index] = .{ .r = @truncate(rgb >> 16), .g = @truncate(rgb >> 8), .b = @truncate(rgb) };
    v.colors.palette.changeDefault(v.gpa(), palette) catch return 1;
    v.flags.dirty.palette = true;
    return 0;
}

export fn yolo_vt_palette_get(t: c.terminal.Terminal, index: u8) u32 {
    const v = c.terminal.zigTerminal(t) orelse return 0;
    const rgb = v.colors.palette.current[index];
    return (@as(u32, @intFromBool(v.colors.palette.mask.isSet(index))) << 24) |
        (@as(u32, rgb.r) << 16) | (@as(u32, rgb.g) << 8) | rgb.b;
}

// A separate, allocation-free native stream observes live replay bytes while
// the holder's terminal tracks only the evicted prefix. Ignore every action
// except executed bells, retaining exact OSC/DCS/UTF-8 parser semantics.
const BellHandler = struct {
    rang: bool = false,
    pub fn deinit(_: *@This()) void {}
    pub fn vt(self: *@This(), comptime action: stream.Action.Tag, _: anytype) void {
        if (action == .bell) self.rang = true;
    }
};
const BellStream = stream.Stream(BellHandler);

export fn yolo_vt_bell_filter_new(t: c.terminal.Terminal) ?*BellStream {
    const v = c.terminal.zigTerminal(t) orelse return null;
    const filter = v.gpa().create(BellStream) catch return null;
    filter.* = .init(.{ .handler = .{} });
    return filter;
}

export fn yolo_vt_bell_filter(filter: *BellStream, input: [*]u8, len: usize) void {
    const data = input[0..len];
    var start: usize = 0;
    for (data, 0..) |b, i| {
        if (b != 7) continue;
        filter.handler.rang = false;
        filter.nextSlice(data[start..i + 1]);
        // NUL is a non-rendering control with the same UTF-8 interruption
        // boundary. Deleting BEL could join invalid UTF-8 into a valid rune.
        if (filter.handler.rang) data[i] = 0;
        start = i + 1;
    }
    filter.nextSlice(data[start..]);
}

// Compatibility output uses the SAME native parser as the canonical terminal.
// Only complete, recognized display/input controls are published. Queries and
// opaque extensions are withheld, so a second physical terminal cannot answer.
const PassthroughHandler = struct {
    allowed: bool = false,
    unsafe: bool = false,
    filtered_modes: bool = false,
    mode_output: [1024]u8 = undefined,
    mode_len: usize = 0,
    pub fn deinit(_: *@This()) void {}
    pub fn vt(self: *@This(), comptime action: stream.Action.Tag, value: anytype) void {
        switch (action) {
            .print, .print_slice, .print_repeat, .bell, .backspace,
            .horizontal_tab, .horizontal_tab_back, .linefeed, .carriage_return,
            .invoke_charset, .cursor_up, .cursor_down, .cursor_left, .cursor_right,
            .cursor_col, .cursor_row, .cursor_col_relative, .cursor_row_relative,
            .cursor_pos, .cursor_style, .erase_display_below, .erase_display_above,
            .erase_display_complete, .erase_display_scrollback, .erase_display_scroll_complete,
            .erase_line_right, .erase_line_left, .erase_line_complete, .erase_line_right_unless_pending_wrap,
            .delete_chars, .erase_chars, .insert_lines, .insert_blanks, .delete_lines,
            .scroll_up, .scroll_down, .tab_clear_current, .tab_clear_all, .tab_set, .tab_reset,
            .index, .next_line, .reverse_index, .full_reset,
            .top_and_bottom_margin, .left_and_right_margin, .left_and_right_margin_ambiguous,
            .save_cursor, .restore_cursor, .modify_key_format, .mouse_shift_capture,
            .protected_mode_off, .protected_mode_iso, .protected_mode_dec,
            .title_push, .title_pop, .kitty_keyboard_push, .kitty_keyboard_pop,
            .kitty_keyboard_set, .kitty_keyboard_set_or, .kitty_keyboard_set_not,
            .end_hyperlink, .start_hyperlink, .active_status_display, .decaln,
            .window_title, .mouse_shape, .configure_charset, .set_attribute,
            .mouse_shape_reset => self.allowed = true,
            .set_mode, .reset_mode, .save_mode, .restore_mode => {
                self.allowed = true;
                // The canonical terminal owns size notifications. Enabling
                // 2048 on a raw viewer would invite duplicate replies now
                // and on every host resize. Preserve other modes in a batch.
                if (value.mode == .in_band_size_reports) {
                    self.filtered_modes = true;
                    return;
                }
                const tag = modes.ModeTag.fromMode(value.mode);
                const final: u8 = switch (action) {
                    .set_mode => 'h', .reset_mode => 'l', .save_mode => 's', .restore_mode => 'r',
                    else => unreachable,
                };
                var writer: std.Io.Writer = .fixed(self.mode_output[self.mode_len..]);
                writer.print("\x1b[{s}{d}{c}", .{ if (tag.ansi) "" else "?", tag.value, final }) catch {
                    self.unsafe = true;
                    return;
                };
                self.mode_len += writer.end;
            },
            .clipboard_contents => {
                if (std.mem.eql(u8, value.data, "?")) self.unsafe = true else self.allowed = true;
            },
            .color_operation => {
                var it = value.requests.constIterator(0);
                while (it.next()) |request| {
                    if (request.* == .query) self.unsafe = true;
                }
                self.allowed = true;
            },
            else => self.unsafe = true,
        }
    }
};
const PassthroughStream = stream.Stream(PassthroughHandler);
const passthrough_limit = 2 * 1024 * 1024;
const Passthrough = struct {
    stream: PassthroughStream,
    allocator: std.heap.FixedBufferAllocator,
    pending: std.Io.Writer.Allocating,
    oversized: bool = false,
};

export fn yolo_vt_passthrough_new(t: c.terminal.Terminal) ?*Passthrough {
    const v = c.terminal.zigTerminal(t) orelse return null;
    const filter = v.gpa().create(Passthrough) catch return null;
    const memory = v.gpa().alloc(u8, passthrough_limit) catch return null;
    filter.* = .{ .stream = undefined, .allocator = .init(memory), .pending = .init(v.gpa()) };
    // Bound the native OSC parser too; an unclosed clipboard payload must not
    // grow forever. All filter allocations belong to the enclosing WASM module.
    filter.stream = .init(.{ .handler = .{}, .allocator = filter.allocator.allocator() });
    return filter;
}

export fn yolo_vt_passthrough(filter: *Passthrough, input: [*]const u8, len: usize) u32 {
    var out: std.Io.Writer.Allocating = .init(filter.pending.allocator);
    defer out.deinit();
    for (input[0..len]) |b| {
        if (filter.pending.written().len < passthrough_limit) {
            filter.pending.writer.writeByte(b) catch return 1;
        } else filter.oversized = true;
        filter.stream.nextSlice(&.{b});
        if (filter.stream.ground()) {
            const pending = filter.pending.written();
            // This is a UTF-8 view. Never forward invalid bytes that another
            // terminal could reinterpret as standalone C1 query introducers.
            if (!filter.oversized and filter.stream.handler.allowed and !filter.stream.handler.unsafe and
                std.unicode.utf8ValidateSlice(pending)) {
                const handler = &filter.stream.handler;
                out.writer.writeAll(if (handler.filtered_modes) handler.mode_output[0..handler.mode_len] else pending) catch return 1;
            }
            filter.pending.clearRetainingCapacity();
            filter.oversized = false;
            filter.stream.handler = .{};
        }
    }
    const data = out.written();
    if (data.len > 0) reply(data.ptr, data.len);
    return 0;
}

export fn yolo_vt_title(t: c.terminal.Terminal, buf: [*]u8, len: usize) usize {
    var title: lib.String = undefined;
    if (c.terminal_get(t, .title, &title) != .success) return 0;
    const n = @min(len, title.len);
    @memcpy(buf[0..n], title.ptr[0..n]);
    return n;
}

// Tracked native references follow scrolling/reflow and explicitly become
// invalid on eviction/reset. Keep Zig layouts inside the bridge.
export fn yolo_vt_anchor(t: c.terminal.Terminal, y: u32) tracked.CTrackedGridRef {
    var ref: tracked.CTrackedGridRef = null;
    if (c.terminal.grid_ref_track(t, point.Point.cval(.{ .screen = .{ .x = 0, .y = y } }), &ref) != .success) return null;
    return ref;
}
export fn yolo_vt_anchor_row(ref: tracked.CTrackedGridRef) u32 {
    const r = ref orelse return 0xfffffffe;
    const v = c.terminal.zigTerminal(r.terminal) orelse return 0xfffffffe;
    if (v.screens.generation(r.screen_key) != r.screen_generation) return 0xfffffffe;
    var coord: point.Coordinate = undefined;
    if (tracked.tracked_grid_ref_point(ref, .screen, &coord) != .success) return 0xffffffff;
    return coord.y;
}
export fn yolo_vt_anchor_free(ref: tracked.CTrackedGridRef) void {
    tracked.tracked_grid_ref_free(ref);
}

// Host timeout/child exit release a hold without injecting input or a VT
// sequence into an unfinished parser control.
export fn yolo_vt_end_hold(t: c.terminal.Terminal) void {
    const v = c.terminal.zigTerminal(t) orelse return;
    v.modes.set(.synchronized_output, false);
}

// Format rows from the active grid (history=false), or from native scrollback.
// Only cell text, SGR, and OSC 8 are emitted: never modes, queries or clipboard
// effects. One call produces a coherent set of rows under the caller's lock.
export fn yolo_vt_rows(t: c.terminal.Terminal, history: bool, start: u32, count: u32, plain: bool) u32 {
    const v = c.terminal.zigTerminal(t) orelse return 1;
    const screen = v.screens.active;
    // Unmodified indices must keep using the outer terminal's theme, even if
    // that terminal does not answer OSC 4. Only application overrides become
    // RGB when the Go adapter receives our canonical formatted SGR output.
    if (!plain) {
        var changed = v.colors.palette.mask.iterator(.{});
        while (changed.next()) |i| {
            const rgb = v.colors.palette.current[i];
            event(4, (@as(u32, @intCast(i)) << 24) | (@as(u32, rgb.r) << 16) | (@as(u32, rgb.g) << 8) | rgb.b);
        }
    }
    var out: std.Io.Writer.Allocating = .init(v.gpa());
    defer out.deinit();
    var segment: std.Io.Writer.Allocating = .init(v.gpa());
    defer segment.deinit();
    var f: formatter.ScreenFormatter = .init(screen, .{
        .emit = if (plain) .plain else .vt, .unwrap = false, .trim = plain,
        .foreground = v.colors.foreground.get(), .background = v.colors.background.get(),
        .palette = null,
    });
    for (start..start + count) |y| {
        const top = screen.pages.pin(if (history) .{ .history = .{ .x = 0, .y = @intCast(y) } } else .{ .active = .{ .x = 0, .y = @intCast(y) } }) orelse return 2;
        out.clearRetainingCapacity();
        // The pinned formatter emits cell hyperlinks only for HTML. Split VT
        // rows into hyperlink runs, retaining native text/style formatting.
        // No raw application controls are forwarded by this rendering path.
        const page = top.node.page();
        const cells = top.cells(.all);
        var x: u16 = 0;
        while (x < v.cols) {
            const link_id = if (!plain and cells[x].hyperlink) page.lookupHyperlink(&cells[x]) else null;
            var end = x + 1;
            while (end < v.cols) : (end += 1) {
                const next_id = if (!plain and cells[end].hyperlink) page.lookupHyperlink(&cells[end]) else null;
                if (next_id != link_id) break;
            }
            var left = top;
            left.x = x;
            var right = top;
            right.x = end - 1;
            f.content = .{ .selection = Selection.init(left, right, true) };
            segment.clearRetainingCapacity();
            f.format(&segment.writer) catch return 3;
            if (link_id) |id| {
                const link = page.hyperlink_set.get(page.memory, id);
                out.writer.writeAll("\x1b]8;") catch return 3;
                switch (link.id) {
                    .implicit => {},
                    .explicit => |s| {
                        out.writer.writeAll("id=") catch return 3;
                        writeLinkText(&out.writer, s.slice(page.memory), true) catch return 3;
                    },
                }
                out.writer.writeByte(';') catch return 3;
                writeLinkText(&out.writer, link.uri.slice(page.memory), false) catch return 3;
                out.writer.writeAll("\x1b\\") catch return 3;
            }
            out.writer.writeAll(std.mem.trimEnd(u8, segment.written(), "\r\n")) catch return 3;
            if (link_id != null) out.writer.writeAll("\x1b]8;;\x1b\\") catch return 3;
            x = end;
        }
        const data = out.written();
        var selected_start: u32 = 0;
        var selected_end: u32 = 0;
        if (screen.selection) |sel| {
            if (sel.containedRow(screen, top)) |span| {
                selected_start = span.start().x;
                selected_end = @as(u32, span.end().x) + 1;
            }
        }
        row(@intCast(y - start), data.ptr, data.len, selected_start, selected_end);
    }
    return 0;
}

// One gesture per terminal, allocated lazily. Native tracked pins own selection
// geometry through reflow, scrolling and screen invalidation. Coordinates here
// are cell centres, not fabricated host pixel reports.
export fn yolo_vt_selection_new(t: c.terminal.Terminal) ?*SelectionGesture {
    const v = c.terminal.zigTerminal(t) orelse return null;
    const gesture = v.gpa().create(SelectionGesture) catch return null;
    gesture.* = .init;
    return gesture;
}

export fn yolo_vt_selection_event(t: c.terminal.Terminal, gesture: *SelectionGesture, action: u32, x: i32, y: u32, view_y: i32, rows: u32, now: u64, rectangle: bool) u32 {
    const v = c.terminal.zigTerminal(t) orelse return 1;
    if (action == 3) {
        gesture.reset(v);
        v.screens.active.clearSelection();
        return 0;
    }
    const pin = v.screens.active.pages.pin(.{ .screen = .{ .x = @intCast(std.math.clamp(x, 0, @as(i32, v.cols) - 1)), .y = y } }) orelse return 1;
    const xpos: f64 = @as(f64, @floatFromInt(x)) * 10 + 5;
    const ypos: f64 = @as(f64, @floatFromInt(view_y)) * 10 + 5;
    switch (action) {
        0 => {
            const sel = gesture.press(v, .{
                .time = .{ .nanoseconds = now }, .pin = pin,
                .xpos = xpos, .ypos = ypos, .max_distance = 10,
                .repeat_interval = 500 * std.time.ns_per_ms,
                .word_boundary_codepoints = &selection_codepoints.default_word_boundaries,
            }) catch return 1;
            v.screens.active.select(sel) catch return 1;
        },
        1 => gesture.release(v, .{ .pin = pin }),
        2 => {
            const sel = gesture.drag(v, .{
                .pin = pin, .xpos = xpos, .ypos = ypos, .rectangle = rectangle,
                .word_boundary_codepoints = &selection_codepoints.default_word_boundaries,
                .geometry = .{ .columns = v.cols, .cell_width = 10, .padding_left = 0, .screen_height = rows * 10 },
            });
            v.screens.active.select(sel) catch return 1;
        },
        else => return 1,
    }
    return 0;
}

export fn yolo_vt_selection_text(t: c.terminal.Terminal, buf: [*]u8, len: usize) u32 {
    const v = c.terminal.zigTerminal(t) orelse return 0xffffffff;
    const sel = v.screens.active.selection orelse return 0;
    var out: std.Io.Writer = .fixed(buf[0..len]);
    var f: formatter.ScreenFormatter = .init(v.screens.active, .{ .emit = .plain, .unwrap = true, .trim = true });
    f.content = .{ .selection = sel };
    f.format(&out) catch return 0xffffffff;
    return @intCast(out.buffered().len);
}

// The selection as the native formatter emits it with styles: the same cells,
// soft-wrap unwrapping, hard breaks and trimming as yolo_vt_selection_text,
// plus SGR for each cell's style, so a caller can recover cell colours (for
// example Kitty placeholder image IDs) in selection order. Only text and SGR
// are emitted: never modes, queries, hyperlinks or clipboard effects.
export fn yolo_vt_selection_vt(t: c.terminal.Terminal, buf: [*]u8, len: usize) u32 {
    const v = c.terminal.zigTerminal(t) orelse return 0xffffffff;
    const sel = v.screens.active.selection orelse return 0;
    var out: std.Io.Writer = .fixed(buf[0..len]);
    // No default colours or palette: the formatter would emit them as OSC
    // 10/11/4 headers. Cells keep their own SGR colours.
    var f: formatter.ScreenFormatter = .init(v.screens.active, .{
        .emit = .vt, .unwrap = true, .trim = true,
        .foreground = null, .background = null, .palette = null,
    });
    f.content = .{ .selection = sel };
    f.format(&out) catch return 0xffffffff;
    return @intCast(out.buffered().len);
}

fn writeLinkText(w: *std.Io.Writer, text: []const u8, parameter: bool) !void {
    for (text) |b| {
        if (b <= 0x20 or b >= 0x7f or (parameter and (b == ';' or b == ':'))) {
            try w.print("%{X:0>2}", .{b});
        } else {
            try w.writeByte(b);
        }
    }
}

// Key names are W3C physical-key names from Ghostty's Key enum; UTF-8 and the
// unshifted codepoint carry the host's logical keyboard layout separately.
export fn yolo_vt_key(t: c.terminal.Terminal, name: [*]const u8, name_len: usize, text: [*]const u8, text_len: usize, codepoint: u32, mods: u16, action: u32, buf: [*]u8, len: usize) u32 {
    const v = c.terminal.zigTerminal(t) orelse return 0xffffffff;
    const k = std.meta.stringToEnum(input_key.Key, name[0..name_len]) orelse return 0xffffffff;
    if (action > 2 or codepoint > 0x10ffff) return 0xffffffff;
    var writer: std.Io.Writer = .fixed(buf[0..len]);
    key_encode.encode(&writer, .{
        .key = k, .utf8 = text[0..text_len], .unshifted_codepoint = @intCast(codepoint),
        .mods = @bitCast(mods), .action = @enumFromInt(action),
    }, .fromTerminal(v)) catch return 0xffffffff;
    return @intCast(writer.end);
}

// Cell formats use unit cells. Pixel reports have already been routed and
// converted from the reporting viewer's font to the canonical measured font.
export fn yolo_vt_mouse(t: c.terminal.Terminal, x: i32, y: i32, button: u32, mods: u16, action: u32, pixels: bool, buf: [*]u8, len: usize) u32 {
    const v = c.terminal.zigTerminal(t) orelse return 0xffffffff;
    if (action > 2 or button > 11) return 0xffffffff;
    const cw: u32 = if (pixels) v.width_px / v.cols else 1;
    const ch: u32 = if (pixels) v.height_px / v.rows else 1;
    if (cw == 0 or ch == 0 or (v.flags.mouse_format == .sgr_pixels and !pixels)) return 0xffffffff;
    var opts: mouse_encode.Options = .fromTerminal(v, .{
        .screen = .{ .width = @as(u32, v.cols) * cw, .height = @as(u32, v.rows) * ch },
        .cell = .{ .width = cw, .height = ch },
        .padding = .{},
    });
    opts.position_is_report = pixels;
    opts.any_button_pressed = button != 0;
    var writer: std.Io.Writer = .fixed(buf[0..len]);
    mouse_encode.encode(&writer, .{
        .pos = .{ .x = @floatFromInt(x), .y = @floatFromInt(y) },
        .button = if (button == 0) null else @as(input_mouse.Button, @enumFromInt(button)),
        .mods = @bitCast(mods), .action = @enumFromInt(action),
    }, opts) catch return 0xffffffff;
    return @intCast(writer.end);
}

// Holder replay models the discarded prefix using the native terminal. The
// grid is disposable: disable scrollback and retain only bounded, replay-safe
// parser continuation. No queries are answered by the Go owner.
export fn yolo_vt_input_tracking(t: c.terminal.Terminal) u32 {
    const history: usize = 0;
    if (c.terminal_set(t, .scrollback_max_bytes, &history) != .success) return 1;
    if (c.terminal_set(t, .continuation_max_bytes, &continuation_limit) != .success) return 2;
    return 0;
}

const input_modes = [_]modes.Mode{
    .cursor_keys, .keypad_keys, .backarrow_key_mode,
    .mouse_event_x10, .mouse_event_normal, .mouse_event_button, .mouse_event_any,
    .focus_event, .mouse_format_utf8, .mouse_format_sgr, .mouse_alternate_scroll,
    .mouse_format_urxvt, .mouse_format_sgr_pixels,
    .ignore_keypad_with_numlock, .alt_esc_prefix, .alt_sends_escape,
    .bracketed_paste,
    .alt_screen_legacy, .alt_screen, .alt_screen_save_cursor_clear_enter,
};

fn writeMode(w: *std.Io.Writer, mode: modes.Mode, enabled: bool) !void {
    try w.print("\x1b[?{d}{s}", .{ modes.ModeTag.fromMode(mode).value, if (enabled) "h" else "l" });
}

fn writeKeyboard(w: *std.Io.Writer, stack: kitty_key.FlagStack) !void {
    // Canonicalize ring rotation, preserving every value reachable by later
    // pops. Leading disabled entries need not consume stack slots. The reset
    // also removes stale application flags in a reused direct-attach terminal.
    try w.writeAll("\x1b[<65535u");
    var first: usize = 0;
    while (first < stack.flags.len - 1 and stack.flags[(@as(usize, stack.idx) + 1 + first) % stack.flags.len].int() == 0) : (first += 1) {}
    for (first..stack.flags.len) |offset| {
        const flags = stack.flags[(@as(usize, stack.idx) + 1 + offset) % stack.flags.len].int();
        try w.print("\x1b[{s}{d}u", .{ if (offset == first) "=" else ">", flags });
    }
}

// DEC mode bits and the effective mouse enum are distinct in this core. A
// later reset of one mouse mode can disable reports while another bit remains
// set. Emit the effective selection last without changing those mode bits.
fn finishMouseModes(w: *std.Io.Writer, v: *c.terminal.ZigTerminal, group: []const modes.Mode, selected: usize) !void {
    if (selected > 0) return writeMode(w, group[selected - 1], true);
    for (group) |mode| {
        if (!v.modes.get(mode)) return writeMode(w, mode, false);
    }
}

fn inputReplay(t: c.terminal.Terminal, w: *std.Io.Writer) !void {
    const v = c.terminal.zigTerminal(t) orelse return error.InvalidTerminal;
    // This prelude runs BEFORE retained output, not after it: switching pages
    // here does not interrupt the tail's cursor, hyperlink or partial CSI.
    try w.writeAll("\x18\x1b[?47l");
    try writeKeyboard(w, v.screens.get(.primary).?.kitty_keyboard);
    try w.writeAll("\x1b[?47h");
    try writeKeyboard(w, if (v.screens.get(.alternate)) |s| s.kitty_keyboard else .{});
    const saved: modes.ModeState = .{ .values = v.modes.saved };
    for (input_modes) |mode| {
        try writeMode(w, mode, saved.get(mode));
        try w.print("\x1b[?{d}s", .{modes.ModeTag.fromMode(mode).value});
        try writeMode(w, mode, v.modes.get(mode));
    }
    // Screen mode bits are independent, too. Re-select the active screen with
    // an operation that leaves those bits unchanged, after saving/restoring
    // all three mode values. The keyboard stacks survive these page switches.
    const alt = v.screens.active_key == .alternate;
    for ([_]modes.Mode{ .alt_screen_legacy, .alt_screen, .alt_screen_save_cursor_clear_enter }) |mode| {
        if (v.modes.get(mode) == alt) {
            try writeMode(w, mode, alt);
            break;
        }
    }
    try finishMouseModes(w, v, &.{ .mouse_event_x10, .mouse_event_normal, .mouse_event_button, .mouse_event_any }, @intCast(@intFromEnum(v.flags.mouse_event)));
    try finishMouseModes(w, v, &.{ .mouse_format_utf8, .mouse_format_sgr, .mouse_format_urxvt, .mouse_format_sgr_pixels }, @intCast(@intFromEnum(v.flags.mouse_format)));
    // XTSHIFTESCAPE has no "restore unspecified" value. Compatibility views
    // use selection override unless the application explicitly captures Shift.
    try w.print("\x1b[>{d}s", .{@as(u8, if (v.flags.mouse_shift_capture == .true) 1 else 0)});
    try w.print("\x1b[>4;{d}m", .{@as(u8, if (v.flags.modify_other_keys_2) 2 else 0)});
    // Native continuation contains only replay-safe unfinished bytes. It may
    // contain a split keyboard sequence or UTF-8 rune at the eviction boundary.
    try c.terminal.continuationWriteIo(t, w);
}

export fn yolo_vt_input_replay(t: c.terminal.Terminal, buf: [*]u8, len: usize) u32 {
    var w: std.Io.Writer = .fixed(buf[0..len]);
    inputReplay(t, &w) catch return 0xffffffff;
    return @intCast(w.end);
}
