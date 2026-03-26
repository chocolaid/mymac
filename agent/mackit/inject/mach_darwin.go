//go:build darwin

package inject

/*
#cgo LDFLAGS: -framework CoreFoundation

#include <mach/mach.h>
#include <dlfcn.h>
#include <string.h>
#include <stdlib.h>
#include <stdio.h>

// mach_vm_* prototypes (not always pulled in by mach/mach.h on all SDK versions)
extern kern_return_t mach_vm_allocate(vm_map_t target, mach_vm_address_t *address,
                                      mach_vm_size_t size, int flags);
extern kern_return_t mach_vm_write(vm_map_t target_task, mach_vm_address_t address,
                                   vm_offset_t data, mach_msg_type_number_t dataCnt);
extern kern_return_t mach_vm_deallocate(vm_map_t target, mach_vm_address_t address,
                                        mach_vm_size_t size);

// inject_result wraps the outcome of a mach_inject call so Go can inspect it.
typedef struct {
    int      kr;         // kern_return_t
    uint64_t remote_path;
    uint64_t remote_stack;
} inject_result_t;

// mach_inject_dylib performs:
//   task_for_pid  → get task port for <pid>
//   mach_vm_allocate × 2 → carve out memory for the dylib path string + a stack
//   mach_vm_write  → copy the path into the target address space
//   thread_create_running → start a remote thread that calls dlopen(path, RTLD_NOW|RTLD_GLOBAL)
//
// The remote thread has lr=0, so it will fault immediately after dlopen returns.
// That fault is intentional and harmless — the dylib constructor has already run.
//
// Return value is kern_return_t (0 = KERN_SUCCESS).
static inject_result_t mach_inject_dylib(int pid, const char *path) {
    inject_result_t res = {0, 0, 0};
    kern_return_t kr;
    task_t task = TASK_NULL;

    kr = task_for_pid(mach_task_self(), pid, &task);
    if (kr != KERN_SUCCESS) {
        res.kr = (int)kr;
        return res;
    }

    // ── Allocate: path buffer ─────────────────────────────────────────────
    mach_vm_size_t  path_size   = (mach_vm_size_t)(strlen(path) + 1);
    mach_vm_address_t remote_path = 0;
    kr = mach_vm_allocate(task, &remote_path, path_size, VM_FLAGS_ANYWHERE);
    if (kr != KERN_SUCCESS) { res.kr = (int)kr; goto cleanup; }
    res.remote_path = (uint64_t)remote_path;

    // ── Allocate: stack (16 KB) ───────────────────────────────────────────
    const mach_vm_size_t stack_size = 16 * 1024;
    mach_vm_address_t remote_stack = 0;
    kr = mach_vm_allocate(task, &remote_stack, stack_size, VM_FLAGS_ANYWHERE);
    if (kr != KERN_SUCCESS) { res.kr = (int)kr; goto cleanup; }
    res.remote_stack = (uint64_t)remote_stack;

    // ── Write path into target ────────────────────────────────────────────
    kr = mach_vm_write(task, remote_path,
                       (vm_offset_t)path, (mach_msg_type_number_t)path_size);
    if (kr != KERN_SUCCESS) { res.kr = (int)kr; goto cleanup; }

    // ── Resolve dlopen in our address space ───────────────────────────────
    // Because all processes share the same dyld shared cache base + ASLR slide
    // (slide is per-boot, not per-process), the absolute VA of dlopen is the
    // same in the target process.
    void *dlopen_sym = dlsym(RTLD_DEFAULT, "dlopen");
    if (!dlopen_sym) { res.kr = (int)KERN_FAILURE; goto cleanup; }

    // ── Launch remote thread ──────────────────────────────────────────────
    thread_t remote_thread;

#if defined(__arm64__)
    arm_thread_state64_t state;
    memset(&state, 0, sizeof(state));
    // ARM64 Procedure Call Standard: x0=arg0, x1=arg1, sp=stack, lr=0, pc=func
    state.__x[0] = (uint64_t)remote_path;               // const char *path
    state.__x[1] = (uint64_t)(RTLD_NOW | RTLD_GLOBAL);  // int mode
    state.__sp   = (uint64_t)(remote_stack + stack_size - 16);
    state.__lr   = 0;   // will fault on return — dylib ctor runs before that
    state.__pc   = (uint64_t)dlopen_sym;
    kr = thread_create_running(task,
                               ARM_THREAD_STATE64,
                               (thread_state_t)&state,
                               ARM_THREAD_STATE64_COUNT,
                               &remote_thread);
#elif defined(__x86_64__)
    x86_thread_state64_t state;
    memset(&state, 0, sizeof(state));
    // x86-64 SysV ABI: rdi=arg0, rsi=arg1, rip=func, rsp aligned to 16 bytes
    // Push a fake return address (0) so the stack is "ret-after-call" aligned.
    uint64_t fake_ret = 0;
    uint64_t sp = remote_stack + stack_size - sizeof(uint64_t);
    mach_vm_write(task, sp, (vm_offset_t)&fake_ret, (mach_msg_type_number_t)sizeof(fake_ret));
    state.__rdi = (uint64_t)remote_path;
    state.__rsi = (uint64_t)(RTLD_NOW | RTLD_GLOBAL);
    state.__rip = (uint64_t)dlopen_sym;
    state.__rsp = sp;
    kr = thread_create_running(task,
                               x86_THREAD_STATE64,
                               (thread_state_t)&state,
                               x86_THREAD_STATE64_COUNT,
                               &remote_thread);
#else
    kr = KERN_NOT_SUPPORTED;
#endif

    res.kr = (int)kr;

cleanup:
    if (task != TASK_NULL)
        mach_port_deallocate(mach_task_self(), task);
    return res;
}

// task_for_pid_kr is a simple wrapper so Go can probe whether task_for_pid
// will succeed for a given pid before attempting injection.
static int task_for_pid_kr(int pid) {
    task_t t = TASK_NULL;
    kern_return_t kr = task_for_pid(mach_task_self(), pid, &t);
    if (kr == KERN_SUCCESS)
        mach_port_deallocate(mach_task_self(), t);
    return (int)kr;
}
*/
import "C"
import (
	"fmt"
	"os"
	"unsafe"
)

// machInjectDylib is the CGo-backed implementation of DylibMach.
func machInjectDylib(pid int, dylibPath string) error {
	// Pre-flight: dylib must exist
	if _, err := os.Stat(dylibPath); err != nil {
		return fmt.Errorf("inject: dylib not found at %s: %w", dylibPath, err)
	}

	// Probe task_for_pid reachability first for a better error message
	if kr := C.task_for_pid_kr(C.int(pid)); kr != 0 {
		return fmt.Errorf(
			"inject: task_for_pid(pid=%d) failed: kern_return_t=%d — "+
				"ensure SIP task_for_pid flag is set (CSRAllowTaskForPID=0x0004) "+
				"or the calling binary holds com.apple.security.cs.debugger",
			pid, int(kr))
	}

	cPath := C.CString(dylibPath)
	defer C.free(unsafe.Pointer(cPath))

	res := C.mach_inject_dylib(C.int(pid), cPath)
	if res.kr != 0 {
		return fmt.Errorf(
			"inject: mach_inject_dylib(pid=%d) failed: kern_return_t=%d",
			pid, int(res.kr))
	}
	return nil
}
