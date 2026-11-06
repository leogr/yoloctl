// SPDX-License-Identifier: MIT
#pragma once
#include <algorithm>
#include <array>
#include <charconv>
#include <cstdint>
#include <string>
#include <string_view>
#include <vector>

namespace yolo {
constexpr size_t max_bytes = 2 << 20;
constexpr size_t max_records = 4096;
constexpr int64_t max_age = 15;
struct Record { std::string id; std::array<std::string, 6> values; };
inline bool hex_id(std::string_view s, size_t n) {
    return s.size() == n && std::all_of(s.begin(), s.end(), [](char c) {
        return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f');
    });
}
inline bool name(std::string_view s, bool box = false) {
    auto alpha = [](char c) { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'); };
    if (s.size() < (box ? 2U : 1U) || s.size() > 63 || !alpha(s[0])) return false;
    if (box && (s == "sessions" || s == "controller" || s == "operators" || s == "operator")) return false;
    return std::all_of(s.begin(), s.end(), [&](char c) { return alpha(c) || c == '.' || c == '_' || c == '-'; });
}
inline std::vector<std::string_view> split(std::string_view s, char separator, size_t limit = 8) {
    std::vector<std::string_view> result;
    size_t start = 0;
    for (;;) {
        auto end = s.find(separator, start);
        if (result.size() >= limit) return {};
        result.emplace_back(s.substr(start, end == s.npos ? end : end - start));
        if (end == s.npos) return result;
        start = end + 1;
    }
}
inline bool number(std::string_view s, int64_t &value) {
    auto parsed = std::from_chars(s.data(), s.data() + s.size(), value);
    return parsed.ec == std::errc() && parsed.ptr == s.data() + s.size() && value >= 0;
}
struct Snapshot {
    int64_t generated = 0;
    std::vector<Record> records;
    std::string generation;
    bool fresh(int64_t now) const { return generated > 0 && generated <= now + 2 && now <= generated + max_age; }
    static Snapshot parse(std::string_view raw, int64_t now) {
        Snapshot empty, result;
        if (raw.empty() || raw.size() > max_bytes || raw.back() != '\n') return empty;
        auto lines = split(raw, '\n', max_records + 2);
        if (lines.empty()) return empty;
        auto header = split(lines[0], '\t');
        int64_t count = 0;
        if (header.size() != 4 || header[0] != "yoloctl-audit-meta-v1" || !hex_id(header[3],64) ||
            !number(header[1], result.generated) || !number(header[2], count) || count > int64_t(max_records) ||
            lines.size() != size_t(count) + 2 || !result.fresh(now)) return empty;
        result.generation = header[3];
        for (size_t i = 1; i <= size_t(count); ++i) {
            auto row = split(lines[i], '\t');
            if (row.size() != 7 || !hex_id(row[0], 64) || !name(row[1], true) || !name(row[2]) || !name(row[3]) || !name(row[4]) ||
                (row[5] != "confined" && row[5] != "privileged") || (row[6] != "true" && row[6] != "false")) return empty;
            if (!result.records.empty() && result.records.back().id >= row[0]) return empty;
            Record record; record.id = row[0];
            for (size_t n = 0; n < record.values.size(); ++n) record.values[n] = row[n + 1];
            result.records.push_back(std::move(record));
        }
        return result;
    }
    const Record *find(std::string_view id, int64_t now) const {
        if (!fresh(now) || (!hex_id(id, 12) && !hex_id(id, 64))) return nullptr;
        auto it = std::lower_bound(records.begin(), records.end(), id, [](const Record &r, std::string_view key) { return r.id < key; });
        if (it == records.end() || it->id.compare(0, id.size(), id) != 0) return nullptr;
        auto next = it + 1;
        if (id.size() == 12 && next != records.end() && next->id.compare(0, id.size(), id) == 0) return nullptr;
        return &*it;
    }
};
} // namespace yolo
