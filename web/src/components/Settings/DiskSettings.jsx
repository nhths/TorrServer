import { useTranslation } from 'react-i18next'
import { FormControl, FormControlLabel, InputLabel, MenuItem, Select, Switch, TextField } from '@material-ui/core'

// DiskSettings is the rare-seed cache + on-disk LRU eviction
// tab. It surfaces only the two fields the backend cares
// about: the on-disk quota in GB (0 disables the policy)
// and the connected-seeder threshold that triggers a
// background full download.
export default function DiskSettings({ settings, inputForm, updateSettings }) {
  const { t } = useTranslation()

  if (!settings) {
    return null
  }

  // Settings are stored in bytes on the server; the UI keeps
  // them in GB to match the rest of the dialog. We never
  // touch settings.DiskCacheBudgetBytes directly — the input
  // handler does the conversion once, on save.
  const budgetGB = Math.round(((settings.DiskCacheBudgetBytes || 0) / (1024 * 1024 * 1024)) * 100) / 100
  const threshold = settings.RareSeedersThreshold ?? 2

  return (
    <>
      <FormControlLabel
        control={
          <Switch
            checked={(settings.DiskCacheBudgetBytes || 0) > 0}
            onChange={(_, checked) => updateSettings({ DiskCacheBudgetBytes: checked ? 1 * 1024 * 1024 * 1024 : 0 })}
          />
        }
        label={t('DiskBudgetEnable', 'Enable rare-seed cache (on-disk quota)')}
      />

      <TextField
        id='DiskCacheBudgetGB'
        type='number'
        inputProps={{ min: 0, max: 1024, step: 1 }}
        value={budgetGB}
        disabled={(settings.DiskCacheBudgetBytes || 0) <= 0}
        onChange={({ target: { value } }) => {
          const gb = Math.max(0, Number(value) || 0)
          updateSettings({ DiskCacheBudgetBytes: gb * 1024 * 1024 * 1024 })
        }}
        label={t('DiskBudgetGB', 'On-disk quota (GB)')}
        helperText={t('DiskBudgetHelper', '0 disables the policy. Oldest torrents are evicted when usage exceeds the quota.')}
        margin='normal'
        fullWidth
      />

      <FormControl margin='normal' fullWidth>
        <InputLabel id='rare-seeders-threshold-label'>
          {t('RareSeedersThreshold', 'Auto-download when connected seeders ≤')}
        </InputLabel>
        <Select
          labelId='rare-seeders-threshold-label'
          id='RareSeedersThreshold'
          value={threshold}
          onChange={({ target: { value } }) => inputForm({ target: { type: 'number', value, id: 'RareSeedersThreshold' } })}
          label={t('RareSeedersThreshold', 'Auto-download when connected seeders ≤')}
          disabled={(settings.DiskCacheBudgetBytes || 0) <= 0}
        >
          <MenuItem value={0}>{t('RareSeedersAtMostZero', '0 (always fetch)').toString()}</MenuItem>
          <MenuItem value={1}>1</MenuItem>
          <MenuItem value={2}>2</MenuItem>
          <MenuItem value={3}>3</MenuItem>
          <MenuItem value={5}>5</MenuItem>
        </Select>
      </FormControl>
    </>
  )
}
