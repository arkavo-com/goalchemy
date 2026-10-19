#!/usr/bin/env python3
"""ESP32-C3 firmware build + QEMU measurements. Run after ESP-IDF export.sh.

  python scripts/bench/micro_c_esp32.py --compiler /tmp/goalchemy \
      --work /tmp/micro-esp32 --json /tmp/micro-esp32.json

Requires ESP-IDF 5.5.1, its ESP32-C3 toolchain and qemu-riscv32. QEMU timing
is emulator timing, not physical-device performance. No hardware is flashed.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import pty
import select
import subprocess
import sys
import time
from micro_c import ROOT, measured, stats

def emulated(build, target):
    subprocess.run([sys.executable, '-m', 'esptool', '--chip=esp32c3', 'merge_bin',
                    '--output=qemu_flash.bin', '--fill-flash-size=2MB', '@flash_args'],
                   cwd=build, check=True, capture_output=True)
    (build/'qemu_efuse.bin').write_bytes(target.default_efuse)
    cmd = [target.qemu_prog, *target.qemu_args.split(), '-drive', 'file=qemu_flash.bin,if=mtd,format=raw',
           '-drive', 'file=qemu_efuse.bin,if=none,format=raw,id=efuse',
           '-global', f'driver={target.efuse_device},property=drive,value=efuse',
           '-global', 'driver=timer.esp32c3.timg,property=wdt_disable,value=true',
           '-nic', 'none', '-nographic', '-serial', 'mon:stdio']
    # QEMU's stdio UART needs a live terminal; a detached/closed stdin can
    # suppress all serial output. Bound the run and close every descriptor.
    master, slave = pty.openpty()
    p = subprocess.Popen(cmd, cwd=build, stdin=slave, stdout=slave, stderr=slave)
    os.close(slave)
    data = bytearray()
    try:
        deadline = time.monotonic()+30
        while time.monotonic()<deadline:
            if select.select([master], [], [], .05)[0]:
                try: data.extend(os.read(master, 65536))
                except OSError: pass  # PTY EOF; process status checked below
            log = data.decode(errors='replace').replace('\r', '')
            match = re.search(r'^goalchemy: (.+)\n', log, re.M)
            if match:
                metrics = {k:int(v) for k,v in re.findall(r'(\w+)=(\d+)', match[1])}
                if metrics['status'] != 0: raise RuntimeError(log)
                return metrics, log
            if p.poll() is not None: raise RuntimeError(log)
        raise TimeoutError(log)
    finally:
        if p.poll() is None: p.terminate()
        p.wait(timeout=10)
        os.close(master)

def main():
    ap=argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--compiler', required=True, type=Path)
    ap.add_argument('--work', required=True, type=Path)
    ap.add_argument('--json', required=True, type=Path)
    ap.add_argument('--runs', type=int, default=3)
    args=ap.parse_args()
    if args.runs<1: ap.error('--runs must be positive')
    idf=Path(os.environ['IDF_PATH'])
    sys.path.insert(0, str(idf/'tools'))
    from idf_py_actions.qemu_ext import QEMU_TARGETS
    env=dict(os.environ, GOMAXPROCS='2', GOMEMLIMIT='1GiB')
    compiler=args.compiler.resolve(); work=args.work.resolve(); work.mkdir(parents=True, exist_ok=True)
    report=dict(revision=subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip(),
                dirty=bool(subprocess.check_output(['git','status','--porcelain'],cwd=ROOT)),
                compiler_sha256=hashlib.sha256(compiler.read_bytes()).hexdigest(),
                idf_revision=subprocess.check_output(['git','rev-parse','HEAD'],cwd=idf,text=True).strip(),
                sdk=subprocess.check_output(['git','describe','--tags','--always'],cwd=idf,text=True).strip(), target='esp32c3', build_jobs=1,
                gcc=subprocess.check_output(['riscv32-esp-elf-gcc','--version'],text=True).splitlines()[0],
                qemu=subprocess.check_output(['qemu-system-riscv32','--version'],text=True).splitlines()[0],
                measurement='QEMU emulation, not hardware; timer watchdog disabled as in IDF qemu. Timer includes UART output. Stack free minimum is bytes; heap free minimum is since boot. Linked RAM excludes dynamically allocated FreeRTOS stacks.',
                workloads={})
    print('workload    QEMU time ms    arena peak B    stack used B    image B', flush=True)
    for name in ('cpu','bytes','maps'):
        src=ROOT/'scripts/bench/testdata'/('micro_'+name); out=work/name; project=out/'esp-idf'; build=project/'build'
        measured([str(compiler),'compile','-target','c-micro','-out',str(out),'.'],src,env)
        measured(['idf.py','-DIDF_TARGET=esp32c3','reconfigure'],project,env)
        build_s, build_rss, _=measured(['cmake','--build','build','-j','1'],project,env)
        size=json.loads(subprocess.check_output([sys.executable,'-m','esp_idf_size','--format','json',str(build/'goalchemy_micro.map')],cwd=project,env=env))
        reference=subprocess.run(['go','run','.'],cwd=src,env=env,check=True,capture_output=True)
        expected=(reference.stdout+reference.stderr).decode().strip()
        samples=[]
        for i in range(args.runs):
            metrics, log=emulated(build,QEMU_TARGETS['esp32c3'])
            (out/f'qemu-{i}.log').write_text(log)
            if '\n'+expected+'\n' not in log: raise RuntimeError(f'{name} differs from Go: {log}')
            samples.append(metrics)
        image=build/'goalchemy_micro.bin'
        stack_bytes=int(re.search(r'^CONFIG_ESP_MAIN_TASK_STACK_SIZE=(\d+)$', (project/'sdkconfig').read_text(), re.M)[1])
        row=dict(build_seconds=build_s, build_peak_host_rss_bytes=build_rss,
                 firmware_bytes=image.stat().st_size, firmware_sha256=hashlib.sha256(image.read_bytes()).hexdigest(),
                 linked_memory=size, samples=samples, qemu_execution_us=stats([s['execution_us'] for s in samples]),
                 task_stack_capacity_bytes=stack_bytes, sdkconfig_sha256=hashlib.sha256((project/'sdkconfig').read_bytes()).hexdigest())
        report['workloads'][name]=row
        print(f"{name:10} {row['qemu_execution_us']['median']/1000:12.3f} {samples[0]['arena_peak_bytes']:15} {stack_bytes-min(s['task_stack_free_min_bytes'] for s in samples):15} {row['firmware_bytes']:10}",flush=True)
    args.json.write_text(json.dumps(report,indent=2)+'\n')

if __name__=='__main__': main()
