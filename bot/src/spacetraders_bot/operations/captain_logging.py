#!/usr/bin/env python3
"""
Captain Log Writer - Automated mission logging and performance tracking with narrative prose
"""

import fcntl
import json
import os
import re
import time
from datetime import datetime, timedelta, timezone

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.helpers.paths import captain_logs_root


class CaptainLogWriter:
    """Automated captain log entry generator"""

    ENTRY_TYPES = {
        'SESSION_START': '🎯',
        'OPERATION_STARTED': '🚀',
        'OPERATION_COMPLETED': '✅',
        'CRITICAL_ERROR': '⚠️',
        'STRATEGIC_DECISION': '🤔',
        'PERFORMANCE_SUMMARY': '📊',
        'SESSION_END': '🎯'
    }

    FORMATTERS = {
        'OPERATION_STARTED': '_format_operation_started',
        'OPERATION_COMPLETED': '_format_operation_completed',
        'CRITICAL_ERROR': '_format_critical_error',
        'PERFORMANCE_SUMMARY': '_format_performance_summary',
    }

    def __init__(self, agent_callsign, token=None):
        """Initialize log writer

        Args:
            agent_callsign: Agent callsign (e.g., CMDR_AC_2025)
            token: Optional API token for fetching agent data
        """
        self.agent_callsign = agent_callsign
        self.token = token
        self.api = APIClient(token) if token else None

        # Paths - store logs in var/logs/captain/{agent}/
        self.agent_dir = captain_logs_root(agent_callsign)
        self.log_file = self.agent_dir / "captain-log.md"
        self.sessions_dir = self.agent_dir / "sessions"
        self.reports_dir = self.agent_dir / "executive_reports"

        # Session state
        self.current_session = None
        self._load_session_state()

    def _load_session_state(self):
        """Load current session state from disk"""
        state_file = self.sessions_dir / "current_session.json"
        if state_file.exists():
            with open(state_file, 'r') as f:
                self.current_session = json.load(f)

    def _save_session_state(self):
        """Save current session state to disk"""
        state_file = self.sessions_dir / "current_session.json"
        with open(state_file, 'w') as f:
            json.dump(self.current_session, f, indent=2)

    def _get_timestamp(self):
        """Get current ISO timestamp"""
        return datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z')

    def _append_to_log(self, content, max_retries=5):
        """Append content to captain log (APPEND-ONLY) with file locking

        Args:
            content: Content to append
            max_retries: Maximum retry attempts for lock acquisition (default: 5)

        Raises:
            IOError: If lock cannot be acquired after max_retries
        """
        for attempt in range(max_retries):
            try:
                with open(self.log_file, 'a') as f:
                    # Acquire exclusive lock (blocks until available)
                    fcntl.flock(f.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
                    try:
                        f.write(content + '\n')
                        f.flush()  # Ensure write completes
                    finally:
                        # Release lock
                        fcntl.flock(f.fileno(), fcntl.LOCK_UN)
                    return  # Success
            except IOError as e:
                if e.errno == 11:  # Resource temporarily unavailable
                    if attempt < max_retries - 1:
                        # Exponential backoff: 0.1s, 0.2s, 0.4s, 0.8s, 1.6s
                        wait_time = 0.1 * (2 ** attempt)
                        time.sleep(wait_time)
                        continue
                    else:
                        raise IOError(f"Failed to acquire log file lock after {max_retries} attempts")
                else:
                    raise  # Other IO errors

    def initialize_log(self):
        """Initialize a new captain log file"""
        if self.log_file.exists():
            print(f"Log already exists: {self.log_file}")
            return

        # Get agent info
        agent_info = self.api.get_agent() if self.api else {}

        content = f"""# CAPTAIN'S LOG - {self.agent_callsign}

## AGENT INFORMATION
- **Callsign:** {self.agent_callsign}
- **Faction:** {agent_info.get('faction', 'UNKNOWN')}
- **Headquarters:** {agent_info.get('headquarters', 'UNKNOWN')}
- **Starting Credits:** {agent_info.get('credits', 0):,}
- **Log Created:** {self._get_timestamp()}

---

## EXECUTIVE SUMMARY

*Session summaries will appear here*

---

## DETAILED LOG ENTRIES

"""
        with open(self.log_file, 'w') as f:
            f.write(content)

        print(f"✅ Initialized log: {self.log_file}")

    def session_start(self, objective, operator="AI First Mate"):
        """Start new session

        Args:
            objective: Mission objective description
            operator: Who started the session
        """
        session_id = datetime.now(timezone.utc).strftime("%Y%m%d_%H%M%S")

        # Get agent status
        agent = self.api.get_agent() if self.api else {}
        ships = self.api.list_ships().get('data', []) if self.api else []
        contracts = self.api.list_contracts().get('data', []) if self.api else []

        active_contracts = [c for c in contracts if c.get('accepted') and not c.get('fulfilled')]

        self.current_session = {
            'session_id': session_id,
            'start_time': self._get_timestamp(),
            'operator': operator,
            'objective': objective,
            'start_credits': agent.get('credits', 0),
            'operations': [],
            'errors': []
        }
        self._save_session_state()

        # Generate log entry
        timestamp = self._get_timestamp()
        entry = f"""
### 📅 STARDATE: {timestamp}

#### {self.ENTRY_TYPES['SESSION_START']} SESSION_START
**Session ID:** {session_id}
**Operator:** {operator}
**Mission:** {objective}

**Starting State:**
- Credits: {agent.get('credits', 0):,}
- Fleet: {len(ships)} ships
- Contracts: {len(active_contracts)} active
- System: {ships[0]['nav']['systemSymbol'] if ships else 'UNKNOWN'}

**Tags:** `#session` `#start`

---
"""
        self._append_to_log(entry)
        print(f"✅ Session started: {session_id}")
        return session_id

    def log_entry(self, entry_type, **kwargs):
        """Create log entry

        Args:
            entry_type: One of ENTRY_TYPES
            **kwargs: Entry-specific parameters
        """
        if entry_type not in self.ENTRY_TYPES:
            raise ValueError(f"Invalid entry type: {entry_type}")

        timestamp = self._get_timestamp()
        icon = self.ENTRY_TYPES[entry_type]

        formatter_name = self.FORMATTERS.get(entry_type)
        if formatter_name:
            formatter = getattr(self, formatter_name)
            entry = formatter(timestamp, icon, **kwargs)
        else:
            entry = self._format_generic_entry(timestamp, icon, entry_type, **kwargs)

        self._append_to_log(entry)

        # Track in session
        if self.current_session:
            if entry_type == 'OPERATION_STARTED':
                self.current_session['operations'].append({
                    'daemon_id': kwargs.get('daemon_id'),
                    'type': kwargs.get('op_type'),
                    'ship': kwargs.get('ship'),
                    'start_time': timestamp
                })
            elif entry_type == 'CRITICAL_ERROR':
                self.current_session['errors'].append({
                    'timestamp': timestamp,
                    'error': kwargs.get('error'),
                    'ship': kwargs.get('ship')
                })
            self._save_session_state()

        print(f"✅ Logged: {entry_type}")

    def _format_operation_started(self, timestamp, icon, **kwargs):
        """Format OPERATION_STARTED entry with narrative prose"""
        operator = kwargs.get('operator', 'Unknown')
        ship = kwargs.get('ship', 'Unknown')
        op_type = kwargs.get('op_type', 'Unknown')
        daemon_id = kwargs.get('daemon_id', 'Unknown')
        params = kwargs.get('parameters', {})
        narrative = kwargs.get('narrative', None)
        tags = kwargs.get('tags', [])

        params_str = '\n'.join([f"- {k}: {v}" for k, v in params.items()])
        tags_str = ' '.join([f"`#{tag}`" for tag in tags])

        # Build narrative section if provided
        narrative_section = ""
        if narrative:
            narrative_section = f"""
**📖 OPERATOR'S LOG:**

{narrative}
"""

        return f"""
### 📅 STARDATE: {timestamp}

#### {icon} OPERATION_STARTED
**Operator:** {operator}
**Ship:** {ship}
**Type:** {op_type}
**Daemon ID:** {daemon_id}
{narrative_section}
**Technical Parameters:**
{params_str}

**Tags:** {tags_str}

---
"""

    def _format_operation_completed(self, timestamp, icon, **kwargs):
        """Format OPERATION_COMPLETED entry with narrative prose"""
        operator = kwargs.get('operator', 'Unknown')
        ship = kwargs.get('ship', 'Unknown')
        duration = kwargs.get('duration', 'Unknown')
        results = kwargs.get('results', {})
        narrative = kwargs.get('narrative', None)
        insights = kwargs.get('insights', None)
        recommendations = kwargs.get('recommendations', None)
        notes = kwargs.get('notes', 'None')
        tags = kwargs.get('tags', [])

        results_table = "| Metric | Value |\n|--------|-------|\n"
        for k, v in results.items():
            results_table += f"| {k} | {v} |\n"

        tags_str = ' '.join([f"`#{tag}`" for tag in tags])

        # Build narrative section if provided
        narrative_section = ""
        if narrative:
            narrative_section = f"""
**📖 MISSION REPORT:**

{narrative}
"""

        # Build insights section if provided
        insights_section = ""
        if insights:
            insights_section = f"""
**💡 STRATEGIC INSIGHTS:**

{insights}
"""

        # Build recommendations section if provided
        recommendations_section = ""
        if recommendations:
            recommendations_section = f"""
**🎯 RECOMMENDATIONS:**

{recommendations}
"""

        return f"""
### 📅 STARDATE: {timestamp}

#### {icon} OPERATION_COMPLETED
**Operator:** {operator}
**Ship:** {ship}
**Duration:** {duration}
{narrative_section}
**Performance Metrics:**
{results_table}
{insights_section}{recommendations_section}
**Technical Notes:**
{notes}

**Tags:** {tags_str}

---
"""

    def _format_critical_error(self, timestamp, icon, **kwargs):
        """Format CRITICAL_ERROR entry with narrative prose"""
        operator = kwargs.get('operator', 'Unknown')
        ship = kwargs.get('ship', 'Unknown')
        error = kwargs.get('error', 'Unknown error')
        narrative = kwargs.get('narrative', None)
        cause = kwargs.get('cause', 'Unknown')
        impact = kwargs.get('impact', {})
        resolution = kwargs.get('resolution', 'None applied')
        lesson = kwargs.get('lesson', 'None')
        escalate = kwargs.get('escalate', False)
        tags = kwargs.get('tags', [])

        impact_str = '\n'.join([f"- {k}: {v}" for k, v in impact.items()])
        tags_str = ' '.join([f"`#{tag}`" for tag in tags])

        # Build narrative section if provided
        narrative_section = ""
        if narrative:
            narrative_section = f"""
**📖 INCIDENT REPORT:**

{narrative}
"""

        return f"""
### 📅 STARDATE: {timestamp}

#### {icon} CRITICAL_ERROR
**Operator:** {operator}
**Ship:** {ship}
**Error:** {error}
{narrative_section}
**Root Cause Analysis:**
{cause}

**Impact Assessment:**
{impact_str}

**Resolution Applied:**
{resolution}

**Lesson Learned:**
{lesson}

**Captain Action Required:** {'YES' if escalate else 'NO'}

**Tags:** {tags_str}

---
"""

    def _format_performance_summary(self, timestamp, icon, **kwargs):
        """Format PERFORMANCE_SUMMARY entry"""
        summary_type = kwargs.get('summary_type', 'Hourly')
        financials = kwargs.get('financials', {})
        operations = kwargs.get('operations', {})
        fleet = kwargs.get('fleet', {})
        top_performers = kwargs.get('top_performers', [])
        tags = kwargs.get('tags', [])

        top_str = '\n'.join([f"{i+1}. {p['ship']}: +{p['profit']:,} cr ({p['operation']})"
                             for i, p in enumerate(top_performers)])
        tags_str = ' '.join([f"`#{tag}`" for tag in tags])

        return f"""
### 📅 STARDATE: {timestamp}

#### {icon} PERFORMANCE_SUMMARY ({summary_type})

**Financials:**
- Revenue This Period: +{financials.get('revenue', 0):,} cr
- Cumulative: +{financials.get('cumulative', 0):,} cr
- Rate: {financials.get('rate', 0):,} cr/hr

**Operations:**
- Completed: {operations.get('completed', 0)}
- Active: {operations.get('active', 0)}
- Success Rate: {operations.get('success_rate', 0)}%

**Fleet Utilization:**
- Active: {fleet.get('active', 0)}/{fleet.get('total', 0)} ships

**Top Performers:**
{top_str}

**Tags:** {tags_str}

---
"""

    def _format_generic_entry(self, timestamp, icon, entry_type, **kwargs):
        """Fallback formatter for entry types without custom renderers."""
        body = '\n'.join(f"- {key}: {value}" for key, value in kwargs.items()) if kwargs else ''

        return f"\n### 📅 STARDATE: {timestamp}\n\n#### {icon} {entry_type}\n\n{body}\n\n---\n"

    def session_end(self):
        """End current session with final report"""
        if not self.current_session:
            print("⚠️  No active session")
            return

        session_id = self.current_session['session_id']
        start_time = datetime.fromisoformat(self.current_session['start_time'].replace('Z', '+00:00'))
        end_time = datetime.now(timezone.utc)
        duration = end_time - start_time

        # Get current agent status
        agent = self.api.get_agent() if self.api else {}
        end_credits = agent.get('credits', 0)
        start_credits = self.current_session['start_credits']
        net_profit = end_credits - start_credits
        roi = (net_profit / start_credits * 100) if start_credits > 0 else 0

        # Calculate stats
        hours = duration.total_seconds() / 3600
        profit_per_hour = net_profit / hours if hours > 0 else 0

        timestamp = self._get_timestamp()

        entry = f"""
### 📅 STARDATE: {timestamp}

#### {self.ENTRY_TYPES['SESSION_END']} SESSION_END
**Session ID:** {session_id}
**Duration:** {int(hours)}h {int((duration.total_seconds() % 3600) / 60)}m

**MISSION COMPLETE**

**Final Statistics:**
| Category | Value |
|----------|-------|
| Starting Credits | {start_credits:,} |
| Ending Credits | {end_credits:,} |
| **Net Profit** | **+{net_profit:,}** |
| **ROI** | **{roi:.1f}%** |
| Operations | {len(self.current_session['operations'])} |
| Errors | {len(self.current_session['errors'])} |
| Avg Profit/hr | {profit_per_hour:,.0f} cr/hr |

**Mission Objective:**
{self.current_session['objective']}

**Tags:** `#session-complete` `#{'profitable' if net_profit > 0 else 'loss'}`

---
"""
        self._append_to_log(entry)

        # Save session archive
        session_file = self.sessions_dir / f"{session_id}.json"
        self.current_session['end_time'] = timestamp
        self.current_session['end_credits'] = end_credits
        self.current_session['net_profit'] = net_profit
        self.current_session['roi'] = roi

        with open(session_file, 'w') as f:
            json.dump(self.current_session, f, indent=2)

        # Clear current session
        self.current_session = None
        (self.sessions_dir / "current_session.json").unlink(missing_ok=True)

        print(f"✅ Session ended: {session_id}")
        print(f"   Net Profit: +{net_profit:,} cr ({roi:.1f}% ROI)")

    def search_logs(self, tag=None, timeframe=None):
        """Search logs by tag and timeframe

        Args:
            tag: Tag to search for (e.g., 'mining', 'error')
            timeframe: Hours to look back

        Returns:
            List of matching entries
        """
        if not self.log_file.exists():
            return []

        with open(self.log_file, 'r') as f:
            content = f.read()

        # Split into entries
        entries = re.split(r'\n### 📅 STARDATE:', content)
        results = []

        for entry in entries[1:]:  # Skip header
            # Check tag
            if tag and f"#{tag}" not in entry:
                continue

            # Check timeframe
            if timeframe:
                timestamp_match = re.search(r'^([0-9T:\-\.Z]+)', entry)
                if timestamp_match:
                    entry_time = datetime.fromisoformat(timestamp_match.group(1).replace('Z', '+00:00'))
                    cutoff = datetime.now(timezone.utc) - timedelta(hours=timeframe)
                    if entry_time < cutoff:
                        continue

            results.append("### 📅 STARDATE:" + entry)

        return results

    def generate_executive_report(self, duration_hours=24):
        """Generate executive summary report

        Args:
            duration_hours: Hours to summarize

        Returns:
            Report markdown string
        """
        cutoff = datetime.now(timezone.utc) - timedelta(hours=duration_hours)

        # Find sessions in timeframe
        sessions = []
        for session_file in self.sessions_dir.glob("*.json"):
            if session_file.name == "current_session.json":
                continue

            with open(session_file, 'r') as f:
                session = json.load(f)

            start_time = datetime.fromisoformat(session['start_time'].replace('Z', '+00:00'))
            if start_time >= cutoff:
                sessions.append(session)

        # Calculate totals
        total_profit = sum(s.get('net_profit', 0) for s in sessions)
        total_operations = sum(len(s.get('operations', [])) for s in sessions)
        total_errors = sum(len(s.get('errors', [])) for s in sessions)

        report = f"""# EXECUTIVE REPORT - {self.agent_callsign}
**Period:** Last {duration_hours} hours
**Generated:** {self._get_timestamp()}

## Summary

**Sessions:** {len(sessions)}
**Total Profit:** +{total_profit:,} cr
**Operations:** {total_operations}
**Errors:** {total_errors}

## Session Breakdown

"""
        for session in sorted(sessions, key=lambda s: s['start_time'], reverse=True):
            duration = 0
            if 'end_time' in session:
                start = datetime.fromisoformat(session['start_time'].replace('Z', '+00:00'))
                end = datetime.fromisoformat(session['end_time'].replace('Z', '+00:00'))
                duration = (end - start).total_seconds() / 3600

            report += f"""### {session['session_id']}
- **Objective:** {session.get('objective', 'Unknown')}
- **Duration:** {duration:.1f}h
- **Profit:** +{session.get('net_profit', 0):,} cr
- **ROI:** {session.get('roi', 0):.1f}%
- **Operations:** {len(session.get('operations', []))}

"""

        return report


def captain_log_operation(args):
    """Main entry point for captain-log operations"""
    from .common import setup_logging
    from spacetraders_bot.core.database import Database

    # Setup logging
    log_file = setup_logging("captain-log", "system", getattr(args, 'log_level', 'INFO'))

    # Get token from player_id if provided
    token = None
    if hasattr(args, 'player_id') and args.player_id:
        db = get_database()
        with db.connection() as conn:
            player = db.get_player_by_id(conn, args.player_id)
            if player:
                token = player['token']

    writer = CaptainLogWriter(args.agent, token)

    if args.action == 'init':
        writer.initialize_log()

    elif args.action == 'session-start':
        writer.session_start(args.objective, args.operator if hasattr(args, 'operator') else "AI First Mate")

    elif args.action == 'session-end':
        writer.session_end()

    elif args.action == 'entry':
        # Build kwargs from args
        kwargs = {}
        if hasattr(args, 'operator'):
            kwargs['operator'] = args.operator
        if hasattr(args, 'ship'):
            kwargs['ship'] = args.ship
        if hasattr(args, 'daemon_id'):
            kwargs['daemon_id'] = args.daemon_id
        if hasattr(args, 'op_type'):
            kwargs['op_type'] = args.op_type
        if hasattr(args, 'narrative'):
            kwargs['narrative'] = args.narrative
        if hasattr(args, 'insights'):
            kwargs['insights'] = args.insights
        if hasattr(args, 'recommendations'):
            kwargs['recommendations'] = args.recommendations
        if hasattr(args, 'error'):
            kwargs['error'] = args.error
        if hasattr(args, 'resolution'):
            kwargs['resolution'] = args.resolution

        writer.log_entry(args.entry_type, **kwargs)

    elif args.action == 'search':
        results = writer.search_logs(
            tag=args.tag if hasattr(args, 'tag') else None,
            timeframe=args.timeframe if hasattr(args, 'timeframe') else None
        )
        for result in results:
            print(result)

    elif args.action == 'report':
        report = writer.generate_executive_report(
            duration_hours=args.duration if hasattr(args, 'duration') else 24
        )

        # Save report
        report_file = writer.reports_dir / f"report_{datetime.now(timezone.utc).strftime('%Y%m%d_%H%M%S')}.md"
        with open(report_file, 'w') as f:
            f.write(report)

        print(report)
        print(f"\n✅ Report saved: {report_file}")

    return 0
