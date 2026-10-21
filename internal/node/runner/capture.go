package runner

// 进程输出采集：stdout 头部预算内全文（FinalAnswer 源）+ 双通道末尾 tail。

// headCap 保留前 limit 字节、丢弃溢出的写入器（截断标记置位）。
type headCap struct {
	buf       []byte
	limit     int
	truncated bool
}

func (h *headCap) Write(p []byte) (int, error) {
	if len(h.buf) >= h.limit {
		h.truncated = h.truncated || len(p) > 0
		return len(p), nil
	}
	n := h.limit - len(h.buf)
	if n > len(p) {
		n = len(p)
	}
	h.buf = append(h.buf, p[:n]...)
	if n < len(p) {
		h.truncated = true
	}
	return len(p), nil
}

func (h *headCap) bytes() []byte { return h.buf }

// tailRing 只保留末尾 capacity 字节的环形缓冲（诊断 tail，SPEC 4.6 64KiB）。
type tailRing struct {
	buf      []byte
	capacity int
	start    int // 最旧字节下标
	n        int // 有效字节数
}

func (r *tailRing) ensureBuf() {
	if r.buf == nil {
		r.buf = make([]byte, r.capacity)
	}
}

func (r *tailRing) Write(p []byte) (int, error) {
	r.ensureBuf()
	total := len(p)
	if total == 0 {
		return 0, nil
	}
	if total >= r.capacity {
		// 一次写入即覆盖整个窗口：只留末尾 capacity 字节
		copy(r.buf, p[total-r.capacity:])
		r.start, r.n = 0, r.capacity
		return total, nil
	}
	if r.n+total > r.capacity {
		// 腾出空间：丢弃最旧字节
		drop := r.n + total - r.capacity
		r.start = (r.start + drop) % r.capacity
		r.n -= drop
	}
	end := (r.start + r.n) % r.capacity
	first := r.capacity - end
	if first > total {
		first = total
	}
	copy(r.buf[end:end+first], p[:first])
	if total > first {
		copy(r.buf[:total-first], p[first:])
	}
	r.n += total
	return total, nil
}

// bytes 返回窗口内末尾字节副本。
func (r *tailRing) bytes() []byte {
	out := make([]byte, r.n)
	for i := 0; i < r.n; i++ {
		out[i] = r.buf[(r.start+i)%r.capacity]
	}
	return out
}
