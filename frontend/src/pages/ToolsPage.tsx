import { Card, CardHeader } from '@/components/ui/Card';
import { ToolTile, type ToolTone } from '@/components/ui/ToolTile';
import { toolGroups, visibleGroups } from '@/components/navigation';
import { useCan } from '@/features/auth/hooks';

/**
 * A hue per group, so the eye can find "the security ones" before it has read
 * a word. Categorical only: nothing here says a tool is healthy or not.
 */
const groupTones: Record<string, ToolTone> = {
  'Hosting Settings': 'blue',
  'Tools & Resources': 'violet',
  Security: 'green',
  'Server Management': 'amber',
  'Logs & Notifications': 'slate',
};

/**
 * ToolsPage is Tools & Settings: every page that administers the server itself,
 * in titled groups, the way Plesk arranges it.
 *
 * It is what lets the sidebar stay short. Nothing that used to be one click away
 * in the sidebar is more than two clicks away here, and the header's search
 * reaches any of it by name.
 */
export function ToolsPage() {
  const groups = visibleGroups(toolGroups, useCan());

  return (
    <div className="space-y-6">
      <header>
        <h1 className="text-xl font-semibold text-ink-strong">Tools &amp; Settings</h1>
        <p className="mt-1 text-sm text-ink-muted">
          Everything that administers this server, rather than one site on it.
        </p>
      </header>

      {/* Columns of groups, not one long list: a group is scanned as a unit,
          and three across puts the whole page above the fold on a desktop. */}
      <div className="grid items-start gap-4 lg:grid-cols-2 2xl:grid-cols-3">
        {groups.map((group) => (
          <Card key={group.title} label={group.title}>
            <CardHeader title={group.title} />
            <ul className="p-2">
              {group.items.map((item) => (
                <li key={item.to}>
                  <ToolTile
                    to={item.to}
                    label={item.label}
                    detail={item.description}
                    tone={groupTones[group.title] ?? 'slate'}
                    icon={<item.icon className="h-4 w-4" />}
                  />
                </li>
              ))}
            </ul>
          </Card>
        ))}
      </div>
    </div>
  );
}
