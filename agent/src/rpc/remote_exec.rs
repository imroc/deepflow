/*
 * Copyright (c) 2024 Yunshan Networks
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

use std::{
    borrow::Cow,
    cell::OnceCell,
    collections::{hash_map::Entry, HashMap, VecDeque},
    fmt::{self, Write as _},
    fs::File,
    io::Write,
    ops::Deref,
    os::unix::fs::MetadataExt,
    path::{Path, PathBuf},
    pin::Pin,
    process::{self, Output},
    ptr,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
    task::{Context, Poll},
    time::{Duration, Instant},
};

use futures::{future::BoxFuture, stream::Stream, TryFutureExt};
use k8s_openapi::api::core::v1::{Event, Pod};
use kube::{
    api::{ListParams, LogParams},
    Api, Client, Config,
};
use log::{debug, info, trace, warn};
use md5::{Digest, Md5};
use parking_lot::RwLock;
use thiserror::Error;
use tokio::{
    process::Command as TokioCommand,
    runtime::Runtime,
    sync::mpsc::{self, Receiver},
    time::{self, Interval},
};

use super::{Session, RPC_RETRY_INTERVAL};
use crate::{exception::ExceptionHandler, trident::AgentId};

use public::{
    netns::{reset_netns, set_netns},
    proto::trident as pb,
};

const MIN_BATCH_LEN: usize = 1024;

#[derive(Clone, Copy)]
enum OutputFormat {
    Text,
    Binary,
}

#[derive(Clone, Copy, PartialEq)]
enum KubeCmd {
    DescribePod,
    Log,
    LogPrevious,
}

#[derive(Clone, Copy, PartialEq)]
enum CommandType {
    Linux,
    Kubernetes(KubeCmd),
}

#[derive(Clone, Copy)]
struct Command {
    cmdline: &'static str,
    output_format: OutputFormat,
    desc: &'static str,
    command_type: CommandType,
}

fn all_supported_commands() -> Vec<Command> {
    vec![
        Command {
            cmdline: "lsns",
            output_format: OutputFormat::Text,
            desc: "",
            command_type: CommandType::Linux,
        },
        Command {
            cmdline: "top -b -n 1 -c -w 512",
            output_format: OutputFormat::Text,
            desc: "top",
            command_type: CommandType::Linux,
        },
        Command {
            cmdline: "ps auxf",
            output_format: OutputFormat::Text,
            desc: "ps",
            command_type: CommandType::Linux,
        },
        Command {
            cmdline: "ip address",
            output_format: OutputFormat::Text,
            desc: "",
            command_type: CommandType::Linux,
        },
        Command {
            cmdline: "kubectl -n $ns describe pod $pod",
            output_format: OutputFormat::Text,
            desc: "",
            command_type: CommandType::Kubernetes(KubeCmd::DescribePod),
        },
        Command {
            cmdline: "kubectl -n $ns logs --tail=10000 $pod",
            output_format: OutputFormat::Text,
            desc: "",
            command_type: CommandType::Kubernetes(KubeCmd::Log),
        },
        Command {
            cmdline: "kubectl -n $ns logs --tail=10000 -p $pod",
            output_format: OutputFormat::Text,
            desc: "",
            command_type: CommandType::Kubernetes(KubeCmd::LogPrevious),
        },
    ]
}

thread_local! {
    static SUPPORTED_COMMANDS: OnceCell<Vec<Command>> = OnceCell::new();
    static MAX_PARAM_NUMS: OnceCell<usize> = OnceCell::new();
}

fn get_cmdline(id: usize) -> Option<&'static str> {
    SUPPORTED_COMMANDS.with(|cell| {
        let cs = cell.get_or_init(|| all_supported_commands());
        cs.get(id).map(|c| c.cmdline)
    })
}

fn get_cmd(id: usize) -> Option<Command> {
    SUPPORTED_COMMANDS.with(|cell| {
        let cs = cell.get_or_init(|| all_supported_commands());
        cs.get(id).copied()
    })
}

fn max_param_nums() -> usize {
    MAX_PARAM_NUMS.with(|p| {
        *p.get_or_init(|| {
            SUPPORTED_COMMANDS.with(|cell| {
                let cs = cell.get_or_init(|| all_supported_commands());
                // count number of dollar args
                cs.iter()
                    .map(|c| {
                        c.cmdline
                            .split_whitespace()
                            .into_iter()
                            .map(|seg| if seg.starts_with('$') { 1 } else { 0 })
                            .sum::<usize>()
                    })
                    .max()
                    .unwrap_or_default()
            })
        })
    })
}

#[derive(Error, Debug)]
pub enum Error {
    #[error("command `{0}` execution failed")]
    CmdExecFailed(#[from] std::io::Error),
    #[error("command `{0}` failed with code {1:?}")]
    CmdFailed(String, Option<i32>),
    #[error("param `{0}` not found")]
    ParamNotFound(String),
    #[error("kubernetes failed with {0}")]
    KubeError(#[from] kube::Error),
    #[error("serialize failed with {0}")]
    SerializeError(#[from] serde_json::Error),
    #[error("transparent")]
    SyscallFailed(String),
}

type Result<T> = std::result::Result<T, Error>;

struct Interior {
    agent_id: Arc<RwLock<AgentId>>,
    session: Arc<Session>,
    exc: ExceptionHandler,
    running: Arc<AtomicBool>,
}

impl Interior {
    async fn run(&mut self) {
        while self.running.load(Ordering::Relaxed) {
            let (sender, receiver) = mpsc::channel(1);
            let responser = Responser::new(self.agent_id.clone(), receiver);

            self.session.update_current_server().await;
            let session_version = self.session.get_version();
            let client = match self.session.get_client() {
                Some(c) => c,
                None => {
                    self.session.set_request_failed(true);
                    tokio::time::sleep(RPC_RETRY_INTERVAL).await;
                    continue;
                }
            };
            let mut client = pb::synchronizer_client::SynchronizerClient::new(client);

            let now = Instant::now();
            trace!("remote_execute call");

            let mut stream = match client.remote_execute(responser).await {
                Ok(stream) => stream,
                Err(e) => {
                    warn!("remote_execute failed: {:?}", e);
                    self.exc.set(pb::Exception::ControllerSocketError);
                    tokio::time::sleep(RPC_RETRY_INTERVAL).await;
                    continue;
                }
            }
            .into_inner();
            trace!("remote_execute initial receive");
            debug!("remote_execute latency {:?}ms", now.elapsed().as_millis());

            while self.running.load(Ordering::Relaxed) {
                let message = stream.message().await;
                let message = match message {
                    Ok(Some(message)) => message,
                    Ok(None) => {
                        debug!("server closed stream");
                        break;
                    }
                    Err(e) => {
                        warn!("remote_execute failed: {:?}", e);
                        self.exc.set(pb::Exception::ControllerSocketError);
                        break;
                    }
                };
                if session_version != self.session.get_version() {
                    info!("grpc server changed");
                    break;
                }
                if message.exec_type.is_none() {
                    continue;
                }
                match pb::ExecutionType::from_i32(message.exec_type.unwrap()) {
                    Some(t) => debug!("received {:?} command from server", t),
                    None => {
                        warn!(
                            "unsupported remote exec type id {}",
                            message.exec_type.unwrap()
                        );
                        continue;
                    }
                }
                if sender.send(message).await.is_err() {
                    debug!("responser channel closed");
                    break;
                }
            }
        }
    }
}

pub struct Executor {
    agent_id: Arc<RwLock<AgentId>>,
    session: Arc<Session>,
    runtime: Arc<Runtime>,
    exc: ExceptionHandler,

    running: Arc<AtomicBool>,
}

impl Executor {
    pub fn new(
        agent_id: Arc<RwLock<AgentId>>,
        session: Arc<Session>,
        runtime: Arc<Runtime>,
        exc: ExceptionHandler,
    ) -> Self {
        Self {
            agent_id,
            session,
            runtime,
            exc,
            running: Default::default(),
        }
    }

    pub fn start(&self) {
        if self.running.swap(true, Ordering::SeqCst) {
            return;
        }
        let mut interior = Interior {
            agent_id: self.agent_id.clone(),
            session: self.session.clone(),
            exc: self.exc.clone(),
            running: self.running.clone(),
        };
        self.runtime.spawn(async move {
            interior.run().await;
        });
        info!("Started remote executor");
    }

    pub fn stop(&self) {
        if !self.running.swap(false, Ordering::SeqCst) {
            return;
        }
        info!("Stopped remote executor");
    }
}

#[derive(Default)]
struct CommandResult {
    request_id: Option<u64>,

    errno: i32,
    output: VecDeque<u8>,
    total_len: usize,
    digest: Md5,
}

struct Responser {
    agent_id: Arc<RwLock<AgentId>>,
    batch_len: usize,

    heartbeat: Interval,
    msg_recv: Receiver<pb::RemoteExecRequest>,

    // request id, future
    pending_lsns: Option<(
        Option<u64>,
        BoxFuture<'static, Result<Vec<pb::LinuxNamespace>>>,
    )>,

    // request id, command id, future
    pending_command: Option<(Option<u64>, usize, BoxFuture<'static, Result<Output>>)>,
    result: CommandResult,
}

impl Responser {
    fn new(agent_id: Arc<RwLock<AgentId>>, receiver: Receiver<pb::RemoteExecRequest>) -> Self {
        Responser {
            agent_id: agent_id,
            batch_len: pb::RemoteExecRequest::default().batch_len() as usize,
            heartbeat: time::interval(Duration::from_secs(30)),
            msg_recv: receiver,
            pending_lsns: None,
            pending_command: None,
            result: CommandResult::default(),
        }
    }

    fn generate_result_batch(&mut self) -> Option<pb::CommandResult> {
        let batch_len = self.batch_len;
        let r = &mut self.result;
        if r.output.is_empty() {
            return None;
        }

        let mut pb_result = pb::CommandResult {
            errno: Some(r.errno),
            total_len: Some(r.total_len as u64),
            pkt_count: Some((r.total_len.saturating_sub(1) / batch_len + 1) as u32),
            ..Default::default()
        };
        let last = r.output.len() <= batch_len;
        if last {
            let content = r.output.drain(..).collect::<Vec<_>>();
            r.digest.update(&content[..]);
            pb_result.content = Some(content);
            pb_result.md5 = Some(format!("{:x}", r.digest.finalize_reset()));
        } else {
            let content = r.output.drain(..batch_len).collect::<Vec<_>>();
            r.digest.update(&content[..]);
            pb_result.content = Some(content);
        }
        Some(pb_result)
    }

    fn command_failed_helper<'a, S: Into<Cow<'a, str>>>(
        &self,
        request_id: Option<u64>,
        code: Option<i32>,
        msg: S,
    ) -> Poll<Option<pb::RemoteExecResponse>> {
        let msg: Cow<str> = msg.into();
        warn!("{}", msg);
        Poll::Ready(Some(pb::RemoteExecResponse {
            agent_id: Some(self.agent_id.read().deref().into()),
            request_id,
            errmsg: Some(msg.into_owned()),
            command_result: Some(pb::CommandResult {
                errno: code,
                ..Default::default()
            }),
            ..Default::default()
        }))
    }
}

impl Stream for Responser {
    type Item = pb::RemoteExecResponse;

    fn poll_next(mut self: Pin<&mut Self>, ctx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        /*
         * order of polling:
         * 1. Send remaining buffered command output
         * 2. Poll pending command if any. If command succeeded, restart from top
         * 3. Poll pending lsns function if any
         * 4. Poll message queue for command from server. On receiving a new command, restart from top
         * 5. Poll ticker for heartbeat
         */

        loop {
            if let Some(batch) = self.as_mut().generate_result_batch() {
                trace!(
                    "send buffer {} bytes",
                    batch.content.as_ref().unwrap().len()
                );
                return Poll::Ready(Some(pb::RemoteExecResponse {
                    agent_id: Some(self.agent_id.read().deref().into()),
                    request_id: self.result.request_id,
                    command_result: Some(batch),
                    ..Default::default()
                }));
            }

            if let Some((_, id, future)) = self.pending_command.as_mut() {
                trace!("poll pending command '{}'", get_cmdline(*id).unwrap());
                let p = future.as_mut().poll(ctx);

                if let Poll::Ready(res) = p {
                    let (request_id, id, _) = self.pending_command.take().unwrap();
                    match res {
                        Ok(output) if output.status.success() => {
                            debug!("command '{}' succeeded", get_cmdline(id).unwrap());
                            if output.stdout.is_empty() {
                                return Poll::Ready(Some(pb::RemoteExecResponse {
                                    agent_id: Some(self.agent_id.read().deref().into()),
                                    request_id: request_id,
                                    command_result: Some(pb::CommandResult::default()),
                                    ..Default::default()
                                }));
                            }
                            let r = &mut self.result;
                            r.request_id = request_id;
                            r.errno = 0;
                            r.output = output.stdout.into();
                            r.total_len = r.output.len();
                            r.digest.reset();
                            continue;
                        }
                        Ok(output) => {
                            if let Some(code) = output.status.code() {
                                return self.command_failed_helper(
                                    request_id,
                                    Some(code),
                                    format!(
                                        "command '{}' failed with {}",
                                        get_cmdline(id).unwrap(),
                                        code
                                    ),
                                );
                            } else {
                                return self.command_failed_helper(
                                    request_id,
                                    None,
                                    format!(
                                        "command '{}' execute terminated without errno",
                                        get_cmdline(id).unwrap()
                                    ),
                                );
                            }
                        }
                        Err(e) => {
                            return self.command_failed_helper(
                                request_id,
                                None,
                                format!(
                                    "command '{}' execute failed: {}",
                                    get_cmdline(id).unwrap(),
                                    e
                                ),
                            )
                        }
                    }
                }
            }

            if let Some((_, future)) = self.pending_lsns.as_mut() {
                trace!("poll pending lsns");
                if let Poll::Ready(result) = future.as_mut().poll(ctx) {
                    let (request_id, _) = self.pending_lsns.take().unwrap();
                    match result {
                        Ok(namespaces) => {
                            debug!("list namespace completed with {} entries", namespaces.len());
                            return Poll::Ready(Some(pb::RemoteExecResponse {
                                agent_id: Some(self.agent_id.read().deref().into()),
                                request_id,
                                linux_namespaces: namespaces,
                                ..Default::default()
                            }));
                        }
                        Err(e) => {
                            warn!("list namespace failed: {}", e);
                            return Poll::Ready(Some(pb::RemoteExecResponse {
                                agent_id: Some(self.agent_id.read().deref().into()),
                                request_id,
                                errmsg: Some(e.to_string()),
                                ..Default::default()
                            }));
                        }
                    }
                }
            }

            match self.msg_recv.poll_recv(ctx) {
                // sender closed, terminate the current stream
                Poll::Ready(None) => return Poll::Ready(None),
                Poll::Ready(Some(msg)) => {
                    match pb::ExecutionType::from_i32(msg.exec_type.unwrap()).unwrap() {
                        pb::ExecutionType::ListCommand => {
                            let mut commands = vec![];
                            SUPPORTED_COMMANDS.with(|cell| {
                                let cs = cell.get_or_init(|| all_supported_commands());
                                for (id, c) in cs.iter().enumerate() {
                                    commands.push(pb::RemoteCommand {
                                        id: Some(id as u32),
                                        cmd: if c.desc.is_empty() {
                                            Some(c.cmdline.to_owned())
                                        } else {
                                            Some(c.desc.to_owned())
                                        },
                                        param_names: c
                                            .cmdline
                                            .split_whitespace()
                                            .filter_map(|seg| {
                                                if seg.starts_with("$") {
                                                    Some(seg.split_at(1).1.to_owned())
                                                } else {
                                                    None
                                                }
                                            })
                                            .collect(),
                                        output_format: match c.output_format {
                                            OutputFormat::Text => {
                                                Some(pb::OutputFormat::Text as i32)
                                            }
                                            OutputFormat::Binary => {
                                                Some(pb::OutputFormat::Binary as i32)
                                            }
                                        },
                                        cmd_type: match c.command_type {
                                            CommandType::Linux => {
                                                Some(pb::CommandType::Linux as i32)
                                            }
                                            CommandType::Kubernetes(_) => {
                                                Some(pb::CommandType::Kubernetes as i32)
                                            }
                                        },
                                    });
                                }
                            });
                            debug!("list command returning {} entries", commands.len());
                            return Poll::Ready(Some(pb::RemoteExecResponse {
                                agent_id: Some(self.agent_id.read().deref().into()),
                                request_id: msg.request_id,
                                commands,
                                ..Default::default()
                            }));
                        }
                        pb::ExecutionType::ListNamespace => {
                            trace!("pending list namespace");
                            self.pending_lsns = Some((msg.request_id, Box::pin(ls_netns())));
                            continue;
                        }
                        pb::ExecutionType::RunCommand => {
                            if let Some(batch_len) = msg.batch_len {
                                self.batch_len = MIN_BATCH_LEN.max(batch_len as usize);
                            }
                            let Some(cmd_id) = msg.command_id else {
                                return self.command_failed_helper(
                                    msg.request_id,
                                    None,
                                    "command_id not specified",
                                );
                            };
                            let Some(cmd) = get_cmd(cmd_id as usize) else {
                                return self.command_failed_helper(
                                    msg.request_id,
                                    None,
                                    "command_id not specified or invalid in run command request",
                                );
                            };
                            let cmdline = &cmd.cmdline;
                            let params =
                                Params(&msg.params[..msg.params.len().min(max_param_nums())]);
                            if !params.is_valid() {
                                return self.command_failed_helper(
                                    msg.request_id,
                                    None,
                                    format!(
                                        "rejected run command '{}' with invalid params: {:?}",
                                        cmdline, params
                                    ),
                                );
                            }

                            let nsfile_fp = match msg.linux_ns_pid {
                                Some(pid) if pid != process::id() => {
                                    let path: PathBuf =
                                        ["/proc", &pid.to_string(), "ns", "net"].iter().collect();
                                    match File::open(&path) {
                                        Ok(fp) => Some(fp),
                                        Err(e) => {
                                            return self.command_failed_helper(
                                                msg.request_id,
                                                None,
                                                format!(
                                                    "open namespace file {} failed: {}",
                                                    path.display(),
                                                    e
                                                ),
                                            )
                                        }
                                    }
                                }
                                _ => None,
                            };

                            trace!(
                                "pending run command '{}', ns_pid: {:?}, params: {:?}",
                                cmdline,
                                msg.linux_ns_pid,
                                params
                            );

                            if *cmdline == "lsns" {
                                self.pending_command = Some((
                                    msg.request_id,
                                    cmd_id as usize,
                                    Box::pin(lsns_command()),
                                ));
                                continue;
                            }

                            match cmd.command_type {
                                CommandType::Kubernetes(kcmd) => {
                                    match kubectl_execute(kcmd, &params) {
                                        Ok(future) => {
                                            self.pending_command =
                                                Some((msg.request_id, cmd_id as usize, future));
                                            continue;
                                        }
                                        Err(e) => {
                                            return self.command_failed_helper(
                                                msg.request_id,
                                                None,
                                                e.to_string(),
                                            )
                                        }
                                    }
                                }
                                _ => (),
                            }

                            // split the whole command line to enable PATH lookup
                            let mut args = cmdline.split_whitespace();
                            let mut cmd = TokioCommand::new(args.next().unwrap());
                            for arg in args {
                                if arg.starts_with('$') {
                                    let name = arg.split_at(1).1;
                                    match params
                                        .0
                                        .iter()
                                        .position(|p| p.key.as_ref().unwrap() == name)
                                    {
                                        Some(pos) => {
                                            cmd.arg(params.0[pos].value.as_ref().unwrap());
                                        }
                                        None => {
                                            return self.command_failed_helper(
                                                msg.request_id,
                                                None,
                                                format!(
                                                    "parameter {} not found in command '{}'",
                                                    arg, cmdline
                                                ),
                                            )
                                        }
                                    }
                                } else {
                                    cmd.arg(arg);
                                }
                            }
                            if let Some(f) = nsfile_fp.as_ref() {
                                if let Err(e) = set_netns(f) {
                                    warn!("set_netns failed when executing {}: {}", cmdline, e);
                                }
                            }
                            let output = cmd.output();
                            if nsfile_fp.is_some() {
                                if let Err(e) = reset_netns() {
                                    warn!("reset_netns failed when executing {}: {}", cmdline, e);
                                }
                            }
                            self.pending_command = Some((
                                msg.request_id,
                                cmd_id as usize,
                                Box::pin(output.map_err(|e| e.into())),
                            ));
                            continue;
                        }
                    }
                }
                _ => (),
            }

            return match self.heartbeat.poll_tick(ctx) {
                Poll::Pending => Poll::Pending,
                Poll::Ready(_) => Poll::Ready(Some(pb::RemoteExecResponse {
                    agent_id: Some(self.agent_id.read().deref().into()),
                    ..Default::default()
                })),
            };
        }
    }
}

const MIN_BUF_SIZE: usize = 1024;

fn username_by_uid(uid: u32) -> Result<String> {
    // SAFTY: sysconf() is unlikely to go wrong
    let conf = unsafe { libc::sysconf(libc::_SC_GETPW_R_SIZE_MAX) };
    let buf_size = if conf < 0 {
        MIN_BUF_SIZE
    } else {
        conf as usize
    };
    #[cfg(target_arch = "x86_64")]
    let mut buffer: Vec<i8> = Vec::with_capacity(buf_size);
    #[cfg(target_arch = "aarch64")]
    let mut buffer: Vec<u8> = Vec::with_capacity(buf_size);
    let mut passwd = libc::passwd {
        pw_name: ptr::null_mut(),
        pw_passwd: ptr::null_mut(),
        pw_uid: 0,
        pw_gid: 0,
        pw_gecos: ptr::null_mut(),
        pw_dir: ptr::null_mut(),
        pw_shell: ptr::null_mut(),
    };
    let mut p_passwd: *mut libc::passwd = ptr::null_mut();
    unsafe {
        // SAFTY: `buffer` is pre-allocated with buf_size for syscall
        //        and will not `Drop` before the end of this function.
        //        The contents in the buffer is `Copy`.
        let r = libc::getpwuid_r(
            uid,
            &mut passwd as *mut libc::passwd,
            buffer.as_mut_ptr(),
            buf_size,
            &mut p_passwd as *mut *mut libc::passwd,
        );
        if r != 0 {
            return Err(Error::SyscallFailed(format!("getpwuid_r failed with {r}")));
        } else if p_passwd.is_null() {
            return Err(Error::SyscallFailed(format!(
                "username with uid {uid} not found"
            )));
        }
        // SAFTY:
        // - p_passwd.pw_name points to nul terminated string in a single allocated `Vec<i8>` object.
        // - The memory referenced will not be mutated.
        Ok(std::ffi::CStr::from_ptr(p_passwd.read().pw_name)
            .to_string_lossy()
            .to_string())
    }
}

async fn get_proc_cmdline<P: AsRef<Path>>(pid_path: P) -> std::io::Result<String> {
    let mut pid_path = pid_path.as_ref().to_path_buf();
    pid_path.push("cmdline");
    let mut cmdline = match tokio::fs::read(&pid_path).await {
        Ok(bytes) => bytes,
        Err(e) => {
            pid_path.pop();
            pid_path.push("comm");
            match tokio::fs::read(&pid_path).await {
                Ok(bytes) => bytes,
                Err(_) => {
                    pid_path.pop();
                    return Err(e);
                }
            }
        }
    };

    // remove trailling \0
    while let Some(c) = cmdline.pop() {
        if c != b'\0' {
            cmdline.push(c);
            break;
        }
    }
    // replace all \0 with space
    for c in cmdline.iter_mut() {
        if *c == b'\0' {
            *c = b' ';
        }
    }
    Ok(String::from_utf8(cmdline).unwrap_or_default())
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum NsType {
    Unknown,
    Mnt,
    Net,
    Pid,
    Uts,
    Ipc,
    User,
    Cgroup,
    Time,
}

impl NsType {
    pub fn as_str(&self) -> &str {
        match self {
            Self::Unknown => "unknown",
            Self::Mnt => "mnt",
            Self::Net => "net",
            Self::Pid => "pid",
            Self::Uts => "uts",
            Self::Ipc => "ipc",
            Self::User => "user",
            Self::Cgroup => "cgroup",
            Self::Time => "time",
        }
    }
}

impl fmt::Display for NsType {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl From<&str> for NsType {
    fn from(s: &str) -> Self {
        match s {
            "mnt" => Self::Mnt,
            "net" => Self::Net,
            "pid" => Self::Pid,
            "uts" => Self::Uts,
            "ipc" => Self::Ipc,
            "user" => Self::User,
            "cgroup" => Self::Cgroup,
            "time" => Self::Time,
            _ => Self::Unknown,
        }
    }
}

#[derive(Debug)]
pub struct Namespace {
    pub id: u64,
    pub ty: NsType,
    pub nprocs: usize,
    pub pid: u32,
    pub user: String,
    pub command: String,
}

impl Namespace {
    pub fn merge(&mut self, mut rhs: Namespace) {
        if self.pid < rhs.pid {
            self.nprocs += 1;
            return;
        }
        rhs.nprocs += 1;
        *self = rhs;
    }
}

impl From<Namespace> for pb::LinuxNamespace {
    fn from(ns: Namespace) -> Self {
        Self {
            id: Some(ns.id),
            pid: Some(ns.pid),
            user: Some(ns.user),
            cmd: Some(ns.command),
            ns_type: Some(ns.ty.to_string()),
        }
    }
}

pub async fn lsns() -> Result<Vec<Namespace>> {
    let mut ns_by_id: HashMap<u64, Namespace> = HashMap::new();
    let mut iter = tokio::fs::read_dir(public::netns::PROC_PATH).await?;
    while let Some(proc) = iter.next_entry().await? {
        match proc.file_type().await {
            Ok(t) if t.is_dir() => (),
            _ => {
                debug!("skipped {}", proc.path().display());
                continue;
            }
        }
        let Some(pid) = proc
            .file_name()
            .to_str()
            .and_then(|s| s.parse::<u32>().ok())
        else {
            continue;
        };
        let mut path = proc.path();

        let user = match tokio::fs::metadata(&path).await {
            Ok(fp) => match username_by_uid(fp.uid()) {
                Ok(name) => name,
                Err(e) => {
                    debug!("get username for uid {} failed: {}", fp.uid(), e);
                    fp.uid().to_string()
                }
            },
            Err(e) => {
                debug!("get uid for process {} failed: {}", pid, e);
                continue;
            }
        };

        let cmdline = match get_proc_cmdline(&path).await {
            Ok(cmdline) => cmdline,
            Err(e) => {
                debug!("get_proc_cmdline for process {} failed: {}", pid, e);
                continue;
            }
        };

        path.push("ns");
        let mut ns_iter = tokio::fs::read_dir(&path).await?;
        while let Some(ns_file) = ns_iter.next_entry().await? {
            let Some(ns_type) = ns_file.file_name().as_os_str().to_str().map(NsType::from) else {
                continue;
            };
            let ns_path = ns_file.path();
            if ns_type == NsType::Unknown {
                debug!("ignored path {} with unknown ns type", ns_path.display());
                continue;
            }

            let Ok(fp) = tokio::fs::metadata(&ns_path).await else {
                continue;
            };

            let nsid = fp.ino();
            let ns = Namespace {
                id: nsid,
                ty: ns_type,
                nprocs: 1,
                pid,
                user: user.clone(),
                command: cmdline.clone(),
            };
            match ns_by_id.entry(nsid) {
                Entry::Occupied(mut o) => o.get_mut().merge(ns),
                Entry::Vacant(v) => {
                    v.insert(ns);
                }
            }
        }
    }
    Ok(ns_by_id.into_values().collect())
}

pub fn write_namespace_table<W: Write>(mut w: W, table: &[Namespace]) -> Result<()> {
    let name_width = table
        .iter()
        .map(|n| n.user.len())
        .max()
        .unwrap_or_default()
        .max("USER".len());
    write!(
        w,
        "        NS TYPE   NPROCS   PID {:<name_width$} COMMAND\n",
        "USER"
    )?;
    for ns in table.iter() {
        write!(
            w,
            "{:>10} {:<6} {:>6} {:>5} {:<name_width$} {}\n",
            ns.id,
            ns.ty.as_str(),
            ns.nprocs,
            ns.pid,
            ns.user,
            ns.command,
        )?;
    }
    Ok(())
}

async fn ls_netns() -> Result<Vec<pb::LinuxNamespace>> {
    Ok(lsns()
        .await?
        .into_iter()
        .filter_map(|ns| {
            if ns.ty == NsType::Net {
                Some(pb::LinuxNamespace::from(ns))
            } else {
                None
            }
        })
        .collect())
}

async fn lsns_command() -> Result<Output> {
    let mut output = vec![];
    write_namespace_table(&mut output, &lsns().await?)?;
    Ok(Output {
        status: Default::default(),
        stdout: output,
        stderr: vec![],
    })
}

struct Params<'a>(&'a [pb::Parameter]);

impl Params<'_> {
    fn is_valid(&self) -> bool {
        for p in self.0.iter() {
            if p.key.is_none() {
                return false;
            }
            let Some(value) = p.value.as_ref() else {
                return false;
            };
            for c in value.as_bytes() {
                match c {
                    b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' => (),
                    _ => return false,
                }
            }
        }
        true
    }
}

impl fmt::Debug for Params<'_> {
    fn fmt(&self, f: &mut fmt::Formatter) -> fmt::Result {
        write!(f, "{{")?;
        let mut empty = true;
        for p in self.0.iter() {
            let Some(key) = p.key.as_ref() else {
                continue;
            };
            if empty {
                write!(f, " ")?;
            } else {
                write!(f, ", ")?;
            }
            if let Some(value) = p.value.as_ref() {
                write!(f, "{}: \"{}\"", key, value)?;
            } else {
                write!(f, "{}: null", key)?;
            }
            empty = false;
        }
        if !empty {
            write!(f, " ")?;
        }
        write!(f, "}}")
    }
}

fn kubectl_execute<'a>(
    cmd: KubeCmd,
    params: &Params<'a>,
) -> Result<BoxFuture<'static, Result<Output>>> {
    // requires `ns` and `pod`
    let mut ns = None;
    let mut pod = None;
    for p in params.0.iter() {
        if let Some(key) = p.key.as_ref() {
            if key == "ns" {
                ns = p.value.clone();
            } else if key == "pod" {
                pod = p.value.clone();
            }
        }
    }
    let Some(ns) = ns else {
        return Err(Error::ParamNotFound("ns".to_owned()));
    };
    let Some(pod) = pod else {
        return Err(Error::ParamNotFound("pod".to_owned()));
    };
    Ok(match cmd {
        KubeCmd::DescribePod => Box::pin(kubectl_describe_pod(ns, pod)),
        KubeCmd::Log => Box::pin(kubectl_log(ns, pod, false)),
        KubeCmd::LogPrevious => Box::pin(kubectl_log(ns, pod, true)),
    })
}

#[derive(Default, serde::Serialize)]
struct DescribePod {
    #[serde(skip_serializing_if = "Option::is_none")]
    pod: Option<Pod>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    events: Vec<Event>,
}

async fn kubectl_describe_pod(namespace: String, pod_name: String) -> Result<Output> {
    let mut config = Config::infer()
        .map_err(|e| kube::Error::InferConfig(e))
        .await?;
    config.accept_invalid_certs = true;
    info!("api server url is: {}", config.cluster_url);
    let client = Client::try_from(config)?;

    let pod = Api::<Pod>::namespaced(client.clone(), &namespace)
        .get(&pod_name)
        .await;

    let mut field_selector =
        format!("involvedObject.name={pod_name},involvedObject.namespace={namespace}");
    if let Some(uid) = pod.as_ref().ok().and_then(|p| p.metadata.uid.as_ref()) {
        let _ = write!(&mut field_selector, ",involvedObject.uid={uid}");
    }
    let events = Api::<Event>::namespaced(client, &namespace)
        .list(&ListParams::default().fields(&field_selector))
        .await;

    let dp = match pod {
        Ok(pod) => DescribePod {
            pod: Some(pod),
            events: events.ok().map(|e| e.items).unwrap_or_default(),
        },
        Err(e) => match events {
            Ok(events) => DescribePod {
                events: events.items,
                ..Default::default()
            },
            Err(_) => {
                return Err(e.into());
            }
        },
    };

    Ok(Output {
        status: Default::default(),
        stdout: serde_json::to_vec_pretty(&dp)?,
        stderr: vec![],
    })
}

const LOG_LINES: usize = 10000;

async fn kubectl_log(namespace: String, pod: String, previous: bool) -> Result<Output> {
    let mut config = Config::infer()
        .map_err(|e| kube::Error::InferConfig(e))
        .await?;
    config.accept_invalid_certs = true;
    info!("api server url is: {}", config.cluster_url);
    let client = Client::try_from(config)?;

    let logs = Api::<Pod>::namespaced(client, &namespace)
        .logs(
            &pod,
            &LogParams {
                previous,
                tail_lines: Some(LOG_LINES as i64),
                ..Default::default()
            },
        )
        .await?;
    Ok(Output {
        status: Default::default(),
        stdout: logs.into_bytes(),
        stderr: vec![],
    })
}
