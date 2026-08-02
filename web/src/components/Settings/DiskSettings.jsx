import { useTranslation } from 'react-i18next'
import { FormControl, FormControlLabel, InputLabel, MenuItem, Select, Switch, TextField } from '@material-ui/core'

// DiskSettings is the rare-seed archive tab. It is
// independent of the streaming cache (UseDisk /
// TorrentsSavePath): a user can stream entirely from RAM
// and still maintain a separate, quota-bounded archive of
// rare torrents. The policy is off when either ArchivePath
// is empty or ArchiveBudgetBytes is zero.
export default function DiskSettings({ settings, inputForm, updateSettings }) {
  const { t } = useTranslation()

  if (!settings) {
    return null
  }

  const budgetGB =
    Math.round(((settings.ArchiveBudgetBytes || 0) / (1024 * 1024 * 1024)) * 100) / 100
  const threshold = settings.RareSeedersThreshold ?? 2
  const tick = settings.RareCacheTickSeconds ?? 60
  const archivePath = settings.ArchivePath || ''
  const archiveEnabled = archivePath !== '' && (settings.ArchiveBudgetBytes || 0) > 0

  return (
    <>
      <TextField
        id='ArchivePath'
        type='text'
        value={archivePath}
        onChange={({ target: { value } }) => updateSettings({ ArchivePath: value })}
        label={t('ArchivePath', 'Archive directory (long-term cache)')}
        helperText={t('ArchivePathHelper', 'Empty = archive policy off. Independent of the streaming cache.')}
        margin='normal'
        fullWidth
      />

      <FormControlLabel
        control={
          <Switch
            checked={archiveEnabled}
            disabled={archivePath === ''}
            onChange={(_, checked) =>
              updateSettings({ ArchiveBudgetBytes: checked ? 1 * 1024 * 1024 * 1024 : 0 })
            }
          />
        }
        label={t('ArchiveBudgetEnable', 'Enable archive quota')}
      />

      <TextField
        id='ArchiveBudgetGB'
        type='number'
        inputProps={{ min: 0, max: 1024, step: 1 }}
        value={budgetGB}
        disabled={!archiveEnabled}
        onChange={({ target: { value } }) => {
          const gb = Math.max(0, Number(value) || 0)
          updateSettings({ ArchiveBudgetBytes: gb * 1024 * 1024 * 1024 })
        }}
        label={t('ArchiveBudgetGB', 'Archive quota (GB)')}
        helperText={t('ArchiveBudgetHelper', 'Oldest pinned torrents are evicted from the archive when usage exceeds the quota.')}
        margin='normal'
        fullWidth
      />

      <FormControl margin='normal' fullWidth>
        <InputLabel id='rare-seeders-threshold-label'>
          {t('RareSeedersThreshold', 'Auto-archive when connected seeders ≤')}
        </InputLabel>
        <Select
          labelId='rare-seeders-threshold-label'
          id='RareSeedersThreshold'
          value={threshold}
          onChange={({ target: { value } }) =>
            inputForm({ target: { type: 'number', value, id: 'RareSeedersThreshold' } })
          }
          label={t('RareSeedersThreshold', 'Auto-archive when connected seeders ≤')}
          disabled={!archiveEnabled}
        >
          <MenuItem value={0}>{t('RareSeedersAtMostZero', '0 (always fetch)').toString()}</MenuItem>
          <MenuItem value={1}>1</MenuItem>
          <MenuItem value={2}>2</MenuItem>
          <MenuItem value={3}>3</MenuItem>
          <MenuItem value={5}>5</MenuItem>
        </Select>
      </FormControl>

      <FormControl margin='normal' fullWidth>
        <InputLabel id='rare-cache-tick-label'>
          {t('RareCacheTickSeconds', 'Ticker period (seconds)')}
        </InputLabel>
        <Select
          labelId='rare-cache-tick-label'
          id='RareCacheTickSeconds'
          value={tick}
          onChange={({ target: { value } }) =>
            inputForm({ target: { type: 'number', value, id: 'RareCacheTickSeconds' } })
          }
          label={t('RareCacheTickSeconds', 'Ticker period (seconds)')}
          disabled={!archiveEnabled}
        >
          <MenuItem value={15}>15</MenuItem>
          <MenuItem value={30}>30</MenuItem>
          <MenuItem value={60}>60</MenuItem>
          <MenuItem value={120}>120</MenuItem>
        </Select>
      </FormControl>
    </>
  )
}
